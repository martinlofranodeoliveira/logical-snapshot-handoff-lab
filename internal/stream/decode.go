// Package stream validates the supported pgoutput subset using pglogrepl.
// It does not implement a replication wire parser.
package stream

import (
	"strconv"

	"github.com/jackc/pglogrepl"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
)

type Decoder struct {
	table       uint32
	relation    bool
	relationRaw []byte
	active      bool
	final       model.LSN
	changes     []model.Change
	raw         [][]byte
	rawBytes    int
}

func New(table uint32) *Decoder { return &Decoder{table: table} }
func (d *Decoder) addRaw(b []byte) error {
	if d.rawBytes+len(b) > model.MaxRaw {
		return model.Fail("TRANSACTION_LIMIT", "raw transaction exceeds 128 KiB")
	}
	d.raw = append(d.raw, append([]byte(nil), b...))
	d.rawBytes += len(b)
	return nil
}
func (d *Decoder) known(id uint32) error {
	if !d.relation || id != d.table {
		return model.Fail("RELATION_LAYOUT", "unknown relation")
	}
	return nil
}
func text(c *pglogrepl.TupleDataColumn) (string, error) {
	if c == nil || c.DataType != 't' {
		return "", model.Fail("UNSUPPORTED_TYPE", "NULL, binary and unchanged TOAST values are not supported")
	}
	return string(c.Data), nil
}
func integer(c *pglogrepl.TupleDataColumn) (int64, error) {
	s, e := text(c)
	if e != nil {
		return 0, e
	}
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || strconv.FormatInt(n, 10) != s {
		return 0, model.Fail("UNSUPPORTED_TYPE", "noncanonical int8 text")
	}
	return n, nil
}
func item(t *pglogrepl.TupleData) (model.Item, error) {
	var r model.Item
	if t == nil || len(t.Columns) != 3 {
		return r, model.Fail("RELATION_LAYOUT", "expected three tuple columns")
	}
	var e error
	r.ID, e = integer(t.Columns[0])
	if e != nil {
		return r, e
	}
	r.Label, e = text(t.Columns[1])
	if e != nil {
		return r, e
	}
	r.Revision, e = integer(t.Columns[2])
	if e != nil {
		return r, e
	}
	return r, nil
}
func key(t *pglogrepl.TupleData) (int64, error) {
	if t == nil || len(t.Columns) != 3 {
		return 0, model.Fail("RELATION_LAYOUT", "expected three key tuple positions")
	}
	for _, c := range t.Columns[1:] {
		if c == nil || c.DataType != 'n' {
			return 0, model.Fail("RELATION_LAYOUT", "non-key data in default-identity old tuple")
		}
	}
	return integer(t.Columns[0])
}

// Feed returns a transaction only at Commit. Raw frames are copied and bounded;
// they are a diagnostic capture, not a persistent audit log.
func (d *Decoder) Feed(data []byte) (tx *model.Transaction, raw [][]byte, err error) {
	defer func() {
		if recover() != nil {
			tx = nil
			raw = nil
			err = model.Fail("INVALID_PROTOCOL", "decoder rejected malformed message")
		}
		if err != nil {
			d.active = false
			d.changes = nil
			d.raw = nil
			d.rawBytes = 0
		}
	}()
	if len(data) == 0 || len(data) > 64*1024 {
		return nil, nil, model.Fail("TRANSACTION_LIMIT", "logical message size outside admitted bound")
	}
	m, e := pglogrepl.Parse(data)
	if e != nil {
		return nil, nil, model.Fail("INVALID_PROTOCOL", "pglogrepl rejected message")
	}
	if b, ok := m.(*pglogrepl.BeginMessage); ok {
		if d.active || b.FinalLSN == 0 || b.Xid == 0 {
			return nil, nil, model.Fail("INVALID_PROTOCOL", "invalid or nested Begin")
		}
		d.active = true
		d.final = model.LSN(b.FinalLSN)
		d.changes = nil
		d.raw = nil
		d.rawBytes = 0
		if d.relationRaw != nil {
			if e = d.addRaw(d.relationRaw); e != nil {
				return nil, nil, e
			}
		}
	}
	if d.active {
		if e = d.addRaw(data); e != nil {
			return nil, nil, e
		}
	}
	switch v := m.(type) {
	case *pglogrepl.BeginMessage:
	case *pglogrepl.RelationMessage:
		d.relation = false
		d.relationRaw = nil
		if v.RelationID != d.table || v.Namespace != "public" || v.RelationName != "items" || v.ReplicaIdentity != 'd' || len(v.Columns) != 3 {
			return nil, nil, model.Fail("RELATION_LAYOUT", "unexpected relation identity")
		}
		names := []string{"id", "label", "revision"}
		oids := []uint32{20, 25, 20}
		for i, c := range v.Columns {
			flag := uint8(0)
			if i == 0 {
				flag = 1
			}
			if c.Name != names[i] || c.DataType != oids[i] || c.TypeModifier != -1 || c.Flags != flag {
				return nil, nil, model.Fail("RELATION_LAYOUT", "unexpected column layout, type, or key flags")
			}
		}
		d.relation = true
		d.relationRaw = append([]byte(nil), data...)
	case *pglogrepl.InsertMessage:
		if !d.active {
			return nil, nil, model.Fail("INVALID_PROTOCOL", "insert outside transaction")
		}
		if e = d.known(v.RelationID); e != nil {
			return nil, nil, e
		}
		r, e := item(v.Tuple)
		if e != nil {
			return nil, nil, e
		}
		d.changes = append(d.changes, model.Change{Kind: "insert", Item: r})
	case *pglogrepl.UpdateMessage:
		if !d.active {
			return nil, nil, model.Fail("INVALID_PROTOCOL", "update outside transaction")
		}
		if e = d.known(v.RelationID); e != nil {
			return nil, nil, e
		}
		if v.OldTuple != nil || v.OldTupleType != 0 {
			return nil, nil, model.Fail("PRIMARY_KEY_MUTATION", "old update key/tuple is unsupported; primary key changes are forbidden")
		}
		r, e := item(v.NewTuple)
		if e != nil {
			return nil, nil, e
		}
		d.changes = append(d.changes, model.Change{Kind: "update", Item: r})
	case *pglogrepl.DeleteMessage:
		if !d.active {
			return nil, nil, model.Fail("INVALID_PROTOCOL", "delete outside transaction")
		}
		if e = d.known(v.RelationID); e != nil {
			return nil, nil, e
		}
		if v.OldTupleType != 'K' {
			return nil, nil, model.Fail("RELATION_LAYOUT", "delete requires default-identity key")
		}
		id, e := key(v.OldTuple)
		if e != nil {
			return nil, nil, e
		}
		d.changes = append(d.changes, model.Change{Kind: "delete", Item: model.Item{ID: id}})
	case *pglogrepl.TruncateMessage:
		if !d.active || len(v.RelationIDs) != 1 || v.Option != 0 {
			return nil, nil, model.Fail("RELATION_LAYOUT", "truncate requires one relation and no options")
		}
		if e = d.known(v.RelationIDs[0]); e != nil {
			return nil, nil, e
		}
		d.changes = append(d.changes, model.Change{Kind: "truncate"})
	case *pglogrepl.CommitMessage:
		end := model.LSN(v.TransactionEndLSN)
		if !d.active || v.Flags != 0 || model.LSN(v.CommitLSN) != d.final || end <= d.final {
			return nil, nil, model.Fail("INVALID_PROTOCOL", "invalid Commit chronology")
		}
		t := model.Transaction{End: end, Changes: d.changes}
		if e = t.Validate(); e != nil {
			return nil, nil, e
		}
		raw = d.raw
		d.active = false
		d.raw = nil
		d.changes = nil
		return &t, raw, nil
	default:
		return nil, nil, model.Fail("UNSUPPORTED_MESSAGE", "message outside supported pgoutput v1 subset")
	}
	if len(d.changes) > model.MaxChanges {
		return nil, nil, model.Fail("TRANSACTION_LIMIT", "more than 64 changes")
	}
	// End=1 is used only to run the same independent payload/row validation.
	if e = (model.Transaction{End: 1, Changes: d.changes}).Validate(); e != nil {
		return nil, nil, e
	}
	return nil, nil, nil
}
