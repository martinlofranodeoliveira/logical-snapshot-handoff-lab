package stream

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
)

// These are synthetic protocol fixtures, independently encoded according to
// PostgreSQL's documented v1 message formats; production uses pglogrepl.Parse.
func number(b *bytes.Buffer, v any) {
	if e := binary.Write(b, binary.BigEndian, v); e != nil {
		panic(e)
	}
}
func relation(oid uint32) []byte {
	var b bytes.Buffer
	b.WriteByte('R')
	number(&b, oid)
	b.WriteString("public\x00items\x00d")
	number(&b, uint16(3))
	for i, n := range []string{"id", "label", "revision"} {
		flag := byte(0)
		typ := uint32(20)
		if i == 0 {
			flag = 1
		}
		if i == 1 {
			typ = 25
		}
		b.WriteByte(flag)
		b.WriteString(n)
		b.WriteByte(0)
		number(&b, typ)
		number(&b, int32(-1))
	}
	return b.Bytes()
}
func begin() []byte {
	var b bytes.Buffer
	b.WriteByte('B')
	number(&b, uint64(100))
	number(&b, uint64(0))
	number(&b, uint32(7))
	return b.Bytes()
}
func commit() []byte {
	var b bytes.Buffer
	b.WriteByte('C')
	b.WriteByte(0)
	number(&b, uint64(100))
	number(&b, uint64(120))
	number(&b, uint64(0))
	return b.Bytes()
}
func tupleWithLabel(b *bytes.Buffer, labelType byte, label string) {
	number(b, uint16(3))
	for i, s := range []string{"1", label, "1"} {
		typ := byte('t')
		if i == 1 {
			typ = labelType
		}
		b.WriteByte(typ)
		if typ == 't' || typ == 'b' {
			number(b, uint32(len(s)))
			b.WriteString(s)
		}
	}
}
func tuple(b *bytes.Buffer, labelType byte) {
	tupleWithLabel(b, labelType, "A")
}
func insertWithLabel(oid uint32, labelType byte, label string) []byte {
	var b bytes.Buffer
	b.WriteByte('I')
	number(&b, oid)
	b.WriteByte('N')
	tupleWithLabel(&b, labelType, label)
	return b.Bytes()
}
func insert(oid uint32, labelType byte) []byte {
	return insertWithLabel(oid, labelType, "A")
}
func TestBufferUntilCommit(t *testing.T) {
	d := New(42)
	for _, b := range [][]byte{begin(), relation(42), insert(42, 't')} {
		tx, _, e := d.Feed(b)
		if e != nil || tx != nil {
			t.Fatal("visible before Commit", tx, e)
		}
	}
	tx, raw, e := d.Feed(commit())
	if e != nil || tx == nil || tx.End != 120 || len(tx.Changes) != 1 || tx.Changes[0].Item.Label != "A" || len(raw) != 4 {
		t.Fatal(tx, len(raw), e)
	}
}
func TestFailClosed(t *testing.T) {
	for _, typ := range []byte{'u', 'n', 'b'} {
		d := New(42)
		d.Feed(begin())
		d.Feed(relation(42))
		if _, _, e := d.Feed(insert(42, typ)); e == nil {
			t.Fatalf("accepted label type %c", typ)
		}
	}
	d := New(42)
	if _, _, e := d.Feed(relation(43)); model.Code(e) != "RELATION_LAYOUT" {
		t.Fatal(e)
	}
	// A valid relation followed by an invalid relation must clear the cached dictionary.
	d = New(42)
	if _, _, e := d.Feed(relation(42)); e != nil {
		t.Fatal(e)
	}
	if _, _, e := d.Feed(relation(43)); model.Code(e) != "RELATION_LAYOUT" {
		t.Fatal(e)
	}
	d.Feed(begin())
	if _, _, e := d.Feed(insert(42, 't')); model.Code(e) != "RELATION_LAYOUT" {
		t.Fatal("invalidated relation remained active", e)
	}
	d = New(42)
	d.Feed(begin())
	if _, _, e := d.Feed(insert(42, 't')); model.Code(e) != "RELATION_LAYOUT" {
		t.Fatal("stale relation assumed", e)
	}
	d = New(42)
	d.Feed(begin())
	d.Feed(relation(42))
	for i := 0; i < 64; i++ {
		if _, _, e := d.Feed(insert(42, 't')); e != nil {
			t.Fatal(i, e)
		}
	}
	if _, _, e := d.Feed(insert(42, 't')); model.Code(e) != "TRANSACTION_LIMIT" {
		t.Fatal(e)
	}
	// Per-row 256-byte bound rejects this large message before another frame is admitted.
	d = New(42)
	d.Feed(begin())
	d.Feed(relation(42))
	bigFrame := insertWithLabel(42, 't', strings.Repeat("x", 33000))
	if _, _, e := d.Feed(bigFrame); model.Code(e) != "INVALID_ROW" {
		t.Fatalf("expected single >256B label to fail INVALID_ROW, got %v", e)
	}
	// Truncate with option flags (e.g. CASCADE=1) must fail closed with RELATION_LAYOUT.
	d = New(42)
	d.Feed(begin())
	d.Feed(relation(42))
	var trunc bytes.Buffer
	trunc.WriteByte('T')
	number(&trunc, uint32(1))
	trunc.WriteByte(1)
	number(&trunc, uint32(42))
	if _, _, e := d.Feed(trunc.Bytes()); model.Code(e) != "RELATION_LAYOUT" {
		t.Fatalf("expected RELATION_LAYOUT for truncate option, got %v", e)
	}
	// Delete with OldTupleType 'O' instead of 'K' must fail closed with RELATION_LAYOUT.
	d = New(42)
	d.Feed(begin())
	d.Feed(relation(42))
	var del bytes.Buffer
	del.WriteByte('D')
	number(&del, uint32(42))
	del.WriteByte('O')
	tuple(&del, 't')
	if _, _, e := d.Feed(del.Bytes()); model.Code(e) != "RELATION_LAYOUT" {
		t.Fatalf("expected RELATION_LAYOUT for non-key delete, got %v", e)
	}
	// Update with old key tuple must fail closed with PRIMARY_KEY_MUTATION.
	d = New(42)
	d.Feed(begin())
	d.Feed(relation(42))
	var b bytes.Buffer
	b.WriteByte('U')
	number(&b, uint32(42))
	b.WriteByte('K')
	number(&b, uint16(3))
	b.WriteByte('t')
	number(&b, uint32(1))
	b.WriteByte('1')
	b.WriteString("nnN")
	tuple(&b, 't')
	if _, _, e := d.Feed(b.Bytes()); model.Code(e) != "PRIMARY_KEY_MUTATION" {
		t.Fatal(e)
	}
	// Commit with EndLSN <= FinalLSN must fail closed with INVALID_PROTOCOL.
	d = New(42)
	d.Feed(begin())
	d.Feed(relation(42))
	var badCommit bytes.Buffer
	badCommit.WriteByte('C')
	badCommit.WriteByte(0)
	number(&badCommit, uint64(100))
	number(&badCommit, uint64(100))
	number(&badCommit, uint64(0))
	if _, _, e := d.Feed(badCommit.Bytes()); model.Code(e) != "INVALID_PROTOCOL" {
		t.Fatalf("expected INVALID_PROTOCOL for bad Commit chronology, got %v", e)
	}
}
func TestMalformedFramesDoNotPanic(t *testing.T) {
	for _, b := range [][]byte{nil, {'?'}, {'B'}, {'R', 0}, {'C', 0}, make([]byte, 65537)} {
		d := New(42)
		if _, _, e := d.Feed(b); e == nil {
			t.Fatalf("accepted malformed frame length %d", len(b))
		}
	}
}
