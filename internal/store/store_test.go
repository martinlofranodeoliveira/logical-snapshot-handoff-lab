package store

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
)

var testID = model.Identity{System: "synthetic-system", Database: 1, Table: 42, Publication: "lab_pub", Slot: "lab_slot"}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, e := Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func snapshot(t *testing.T, s *Store) model.State {
	t.Helper()
	st, e := s.Snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return st
}
func TestUnsignedDurabilityAndDuplicates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, e := Open(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Bootstrap(ctx, testID, 0x7fffffffffffffff, nil, nil); e != nil {
		t.Fatal(e)
	}
	first := model.Transaction{End: 0x8000000000000000, Changes: []model.Change{{Kind: "insert", Item: model.Item{ID: 7, Label: "ação", Revision: 1}}}}
	if ok, e := s.Apply(ctx, first, nil); e != nil || !ok {
		t.Fatal(ok, e)
	}
	before := snapshot(t, s)
	if ok, e := s.Apply(ctx, first, nil); e != nil || ok {
		t.Fatal("duplicate", ok, e)
	}
	if after := snapshot(t, s); !reflect.DeepEqual(before, after) {
		t.Fatal("duplicate changed durable state")
	}
	last := model.Transaction{End: ^model.LSN(0), Changes: []model.Change{{Kind: "update", Item: model.Item{ID: 7, Label: "final", Revision: 2}}}}
	if ok, e := s.Apply(ctx, last, nil); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	st := snapshot(t, s)
	if st.Checkpoint != ^model.LSN(0) || st.Applied != 2 || len(st.Rows) != 1 || st.Rows[0].Label != "final" {
		t.Fatalf("reopen: %+v", st)
	}
}
func TestFailuresKeepWholeOldImage(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if e := s.Bootstrap(ctx, testID, 10, []model.Item{{ID: 1, Label: "old", Revision: 1}}, nil); e != nil {
		t.Fatal(e)
	}
	before := snapshot(t, s)
	tests := []model.Transaction{
		{End: 11, Changes: []model.Change{{Kind: "delete", Item: model.Item{ID: 1}}, {Kind: "update", Item: model.Item{ID: 99, Label: "missing", Revision: 1}}}},
		{End: 11, Changes: []model.Change{{Kind: "insert", Item: model.Item{ID: 1, Label: "duplicate", Revision: 1}}}},
		{End: 11, Changes: []model.Change{{Kind: "update", Item: model.Item{ID: 1, Label: "bad", Revision: 0}}}},
	}
	many := model.Transaction{End: 11}
	for i := 0; i < 65; i++ {
		many.Changes = append(many.Changes, model.Change{Kind: "truncate"})
	}
	tests = append(tests, many)
	rows := model.Transaction{End: 11}
	for i := int64(2); i <= 33; i++ {
		rows.Changes = append(rows.Changes, model.Change{Kind: "insert", Item: model.Item{ID: i, Label: "x", Revision: 1}})
	}
	tests = append(tests, rows)
	for _, tx := range tests {
		if _, e := s.Apply(ctx, tx, nil); e == nil {
			t.Fatal("accepted invalid transaction")
		}
		if !reflect.DeepEqual(before, snapshot(t, s)) {
			t.Fatal("failed transaction leaked partial state")
		}
	}
	hook := func(string) error { return errors.New("synthetic commit barrier failure") }
	if _, e := s.Apply(ctx, model.Transaction{End: 11, Changes: []model.Change{{Kind: "truncate"}}}, hook); e == nil {
		t.Fatal("ignored hook")
	}
	if !reflect.DeepEqual(before, snapshot(t, s)) {
		t.Fatal("precommit failure leaked")
	}
	tooMany := make([]model.Item, 33)
	for i := range tooMany {
		tooMany[i] = model.Item{ID: int64(i), Label: "x", Revision: 1}
	}
	if e := s.Bootstrap(ctx, testID, 100, tooMany, nil); e == nil {
		t.Fatal("accepted oversized snapshot")
	}
	if !reflect.DeepEqual(before, snapshot(t, s)) {
		t.Fatal("bootstrap destroyed old image")
	}
}
func TestIntermediateRowCountAndCounterLimit(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	initial := make([]model.Item, model.MaxRows)
	for i := range initial {
		initial[i] = model.Item{ID: int64(i + 1), Label: "r", Revision: 1}
	}
	if e := s.Bootstrap(ctx, testID, 10, initial, nil); e != nil {
		t.Fatal(e)
	}
	// Intermediate state reaches 33 rows before deleting row 1 in the same transaction.
	tx := model.Transaction{
		End: 11,
		Changes: []model.Change{
			{Kind: "insert", Item: model.Item{ID: 33, Label: "new", Revision: 1}},
			{Kind: "delete", Item: model.Item{ID: 1}},
		},
	}
	if ok, e := s.Apply(ctx, tx, nil); e != nil || !ok {
		t.Fatalf("expected intermediate 33-row transaction with 32 final rows to succeed: ok=%v err=%v", ok, e)
	}
	if _, e := s.db.ExecContext(ctx, "UPDATE meta SET applied = ? WHERE singleton = 1", int64(math.MaxInt64)); e != nil {
		t.Fatal(e)
	}
	before := snapshot(t, s)
	if _, e := s.Apply(ctx, model.Transaction{End: 12, Changes: []model.Change{{Kind: "update", Item: model.Item{ID: 33, Label: "overflow", Revision: 2}}}}, nil); model.Code(e) != "COUNTER_LIMIT" {
		t.Fatalf("expected COUNTER_LIMIT, got %v", e)
	}
	if !reflect.DeepEqual(before, snapshot(t, s)) {
		t.Fatal("COUNTER_LIMIT mutated durable projection")
	}
}
func TestWriterFence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, e := Open(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	other, e := Open(ctx, root)
	if other != nil {
		other.Close()
	}
	if model.Code(e) != "WRITER_BUSY" {
		t.Fatal("missing exclusive fence", e)
	}
}
