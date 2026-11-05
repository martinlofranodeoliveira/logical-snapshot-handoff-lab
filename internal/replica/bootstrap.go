package replica

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/source"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/store"
)

var snapshotName = regexp.MustCompile(`^[0-9A-Fa-f-]{1,80}$`)

func dropSlotIfPresent(ctx context.Context, c *pgx.Conn) error {
	for {
		var exists, active bool
		if e := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_replication_slots WHERE slot_name='lab_slot'),
 COALESCE((SELECT active FROM pg_replication_slots WHERE slot_name='lab_slot'), false)`).Scan(&exists, &active); e != nil {
			return e
		}
		if !exists {
			return nil
		}
		if !active {
			_, e := c.Exec(ctx, "SELECT pg_drop_replication_slot('lab_slot')")
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Bootstrap is explicit, including when replacing a slot. Only the private
// source can be reached. The old SQLite image survives every precommit failure.
func (r *Replica) Bootstrap(ctx context.Context, hook store.Hook) (err error) {
	defer func() {
		if err == nil {
			return
		}
		var me *model.Error
		if errors.As(err, &me) {
			return
		}
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, os.ErrDeadlineExceeded) {
			err = model.Fail("INCOMPLETE", "deadline/cancellation before bootstrap completion")
		}
	}()
	c, e := source.Connect(ctx, r.root)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	id, e := source.Identity(ctx, c)
	if e != nil {
		return e
	}
	if e = dropSlotIfPresent(ctx, c); e != nil {
		return e
	}
	exporter, e := source.Replication(ctx, r.root)
	if e != nil {
		return e
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = exporter.Close(closeCtx)
	}()
	slot, e := pglogrepl.CreateReplicationSlot(ctx, exporter, source.Slot, "pgoutput", pglogrepl.CreateReplicationSlotOptions{Mode: pglogrepl.LogicalReplication, SnapshotAction: "EXPORT_SNAPSHOT"})
	if e != nil {
		return e
	}
	cp, e := model.ParseLSN(slot.ConsistentPoint)
	if e != nil {
		return e
	}
	if !snapshotName.MatchString(slot.SnapshotName) {
		return model.Fail("INVALID_SNAPSHOT", "unexpected exported snapshot identifier")
	}
	tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	// Absolutely no data query on this transaction precedes the import. No
	// command is sent on exporter until the importing transaction has finished.
	if _, e = tx.Exec(ctx, "SET TRANSACTION SNAPSHOT '"+slot.SnapshotName+"'"); e != nil {
		return e
	}
	if hook != nil {
		if e = hook("snapshot"); e != nil {
			return e
		}
	}
	rows, e := tx.Query(ctx, "SELECT id,label,revision FROM public.items ORDER BY id LIMIT 33")
	if e != nil {
		return e
	}
	image := make([]model.Item, 0, model.MaxRows)
	for rows.Next() {
		var row model.Item
		if e = rows.Scan(&row.ID, &row.Label, &row.Revision); e != nil {
			rows.Close()
			return e
		}
		if e = row.Validate(); e != nil {
			rows.Close()
			return e
		}
		image = append(image, row)
		if len(image) > model.MaxRows {
			rows.Close()
			return model.Fail("TRANSACTION_LIMIT", "snapshot exceeds 32 rows")
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return fmt.Errorf("source snapshot read: %w", e)
	}
	r.Capture = nil
	return r.store.Bootstrap(ctx, id, cp, image, hook)
}
