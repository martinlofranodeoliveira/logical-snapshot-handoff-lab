package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
)

type Hook func(string) error

func hit(h Hook, point string) error {
	if h != nil {
		return h(point)
	}
	return nil
}

// Bootstrap replaces a complete image and its identity in one transaction.
// Failure never deletes the previously committed image.
func (s *Store) Bootstrap(ctx context.Context, id model.Identity, cp model.LSN, rows []model.Item, hook Hook) error {
	if cp == 0 || id.System == "" || id.Database == 0 || id.Table == 0 || id.Publication != "lab_pub" || id.Slot != "lab_slot" {
		return model.Fail("INVALID_BOOTSTRAP", "complete identity and nonzero consistent point required")
	}
	if len(rows) > model.MaxRows {
		return model.Fail("TRANSACTION_LIMIT", "snapshot exceeds 32 rows")
	}
	for _, r := range rows {
		if e := r.Validate(); e != nil {
			return e
		}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "DELETE FROM items"); e != nil {
		return e
	}
	for _, r := range rows {
		if _, e = tx.ExecContext(ctx, "INSERT INTO items VALUES(?,?,?)", r.ID, r.Label, r.Revision); e != nil {
			return model.Fail("INVALID_BOOTSTRAP", "duplicate or invalid snapshot row")
		}
	}
	b, e := json.Marshal(id)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta VALUES(1,?,?,0)", string(b), cp.Hex()); e != nil {
		return e
	}
	if e = hit(hook, "commit"); e != nil {
		return e
	}
	return tx.Commit()
}

// Apply advances only to a transaction's Commit.EndLSN, never a transport LSN.
func (s *Store) Apply(ctx context.Context, t model.Transaction, hook Hook) (bool, error) {
	if e := t.Validate(); e != nil {
		return false, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	st, e := readMeta(ctx, tx)
	if e != nil {
		return false, e
	}
	if !st.Initialized {
		return false, model.Fail("REBOOTSTRAP_REQUIRED", "no bootstrap checkpoint")
	}
	if t.End <= st.Checkpoint {
		return false, nil
	}
	if st.Applied == math.MaxInt64 {
		return false, model.Fail("COUNTER_LIMIT", "applied transaction counter exhausted")
	}
	for _, c := range t.Changes {
		var affected int64
		switch c.Kind {
		case "truncate":
			_, e = tx.ExecContext(ctx, "DELETE FROM items")
		case "insert":
			_, e = tx.ExecContext(ctx, "INSERT INTO items VALUES(?,?,?)", c.Item.ID, c.Item.Label, c.Item.Revision)
		case "update":
			r, x := tx.ExecContext(ctx, "UPDATE items SET label=?,revision=? WHERE id=?", c.Item.Label, c.Item.Revision, c.Item.ID)
			e = x
			if e == nil {
				affected, e = r.RowsAffected()
				if e == nil && affected != 1 {
					e = model.Fail("APPLY_INVALID", "update key is absent")
				}
			}
		case "delete":
			r, x := tx.ExecContext(ctx, "DELETE FROM items WHERE id=?", c.Item.ID)
			e = x
			if e == nil {
				affected, e = r.RowsAffected()
				if e == nil && affected != 1 {
					e = model.Fail("APPLY_INVALID", "delete key is absent")
				}
			}
		}
		if e != nil {
			if ctx.Err() != nil || errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
				return false, e
			}
			return false, model.Fail("APPLY_INVALID", "projection constraint or row-presence failure")
		}
	}
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM items").Scan(&count); e != nil {
		return false, e
	}
	if count > model.MaxRows {
		return false, model.Fail("TRANSACTION_LIMIT", "final image exceeds 32 rows")
	}
	if _, e = tx.ExecContext(ctx, "UPDATE meta SET checkpoint=?,applied=applied+1 WHERE singleton=1", t.End.Hex()); e != nil {
		return false, e
	}
	if e = hit(hook, "commit"); e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	return true, nil
}
