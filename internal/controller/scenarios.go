// Package controller runs independent SQL oracles against one owned source and
// separate replicator processes. Scenarios are sequential, never concurrent labs.
package controller

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/rpc"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/source"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/store"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/stream"
)

type Report struct {
	Version        string    `json:"version"`
	Scenarios      []string  `json:"passed_scenarios"`
	SQLiteVersion  string    `json:"sqlite_version"`
	SQLiteSourceID string    `json:"sqlite_source_id"`
	Checkpoint     model.LSN `json:"checkpoint"`
	Incomplete     bool      `json:"incomplete"`
	Error          string    `json:"error,omitempty"`
}
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func projectionRows(ctx context.Context, q queryer) ([]model.Item, error) {
	rows, e := q.QueryContext(ctx, "SELECT id,label,revision FROM items ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []model.Item{}
	for rows.Next() {
		var r model.Item
		if e = rows.Scan(&r.ID, &r.Label, &r.Revision); e != nil {
			return nil, e
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func sourceRows(ctx context.Context, c *pgx.Conn) ([]model.Item, error) {
	rows, e := c.Query(ctx, "SELECT id,label,revision FROM public.items ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []model.Item{}
	for rows.Next() {
		var r model.Item
		if e = rows.Scan(&r.ID, &r.Label, &r.Revision); e != nil {
			return nil, e
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func equalSQL(ctx context.Context, c *pgx.Conn, db *sql.DB, want []model.Item) error {
	src, e := sourceRows(ctx, c)
	if e != nil {
		return e
	}
	dst, e := projectionRows(ctx, db)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(src, want) || !reflect.DeepEqual(dst, want) {
		return fmt.Errorf("independent SQL equality failed: source=%v projection=%v expected=%v", src, dst, want)
	}
	return nil
}
func unchanged(a, b model.State) bool {
	return a.Checkpoint == b.Checkpoint && a.Applied == b.Applied && reflect.DeepEqual(a.Rows, b.Rows)
}
func replayCapture(ctx context.Context, baseline model.State, capture [][]byte) error {
	root, e := os.MkdirTemp("/tmp", "lsh-replay-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(root)
	s, e := store.Open(ctx, root)
	if e != nil {
		return e
	}
	defer s.Close()
	if e = s.Bootstrap(ctx, baseline.Identity, baseline.Checkpoint, baseline.Rows, nil); e != nil {
		return e
	}
	d := stream.New(baseline.Identity.Table)
	var tx *model.Transaction
	for _, raw := range capture {
		decoded, _, e := d.Feed(raw)
		if e != nil {
			return e
		}
		if decoded != nil {
			if tx != nil {
				return fmt.Errorf("capture contains multiple transactions")
			}
			tx = decoded
		}
	}
	if tx == nil {
		return fmt.Errorf("capture has no Commit")
	}
	applied, e := s.Apply(ctx, *tx, nil)
	if e != nil || !applied {
		return fmt.Errorf("first captured replay: applied=%v error=%v", applied, e)
	}
	first, e := s.Snapshot(ctx)
	if e != nil {
		return e
	}
	if first.Applied != 1 || len(first.Rows) != 2 || first.Rows[0].Label != "A" || first.Rows[1].Label != "B" {
		return fmt.Errorf("captured replay has wrong independent expected image")
	}
	applied, e = s.Apply(ctx, *tx, nil)
	if e != nil || applied {
		return fmt.Errorf("duplicate captured replay changed state: %v", e)
	}
	second, e := s.Snapshot(ctx)
	if e != nil {
		return e
	}
	if !unchanged(first, second) {
		return fmt.Errorf("duplicate changed rows, checkpoint or count")
	}
	return nil
}

func Run(ctx context.Context, exe string, args []string) (report Report, err error) {
	report = Report{Version: "0.1.0", Scenarios: []string{}}
	defer func() {
		if err != nil {
			report.Incomplete = true
			report.Error = model.Diagnostic(err)
		}
	}()
	cluster, e := source.Start(ctx)
	if e != nil {
		return report, e
	}
	defer func() {
		if e := cluster.Close(); e != nil && err == nil {
			err = e
		}
	}()
	c, e := source.Connect(ctx, cluster.Root)
	if e != nil {
		return report, e
	}
	defer c.Close(context.Background())
	var w *worker
	defer func() {
		if w != nil {
			if e := w.close(); e != nil && err == nil {
				err = e
			}
		}
	}()
	w, e = startWorker(cluster.Root, exe, args)
	if e != nil {
		return report, e
	}
	if e = w.send(rpc.Request{Action: "bootstrap", Barrier: "snapshot"}); e != nil {
		return report, e
	}
	if e = w.event(ctx, "snapshot"); e != nil {
		return report, e
	}
	if _, e = c.Exec(ctx, "INSERT INTO public.items VALUES(2,'B',1)"); e != nil {
		return report, e
	}
	if e = w.release(); e != nil {
		return report, e
	}
	boot, e := requireOK(w.read(ctx))
	if e != nil {
		return report, e
	}
	if len(boot.State.Rows) != 1 || boot.State.Rows[0].Label != "A" {
		return report, fmt.Errorf("snapshot included post-import row")
	}
	report.SQLiteVersion, report.SQLiteSourceID = boot.State.SQLiteVersion, boot.State.SQLiteSourceID
	report.Checkpoint = boot.State.Checkpoint
	caught, e := requireOK(w.call(ctx, rpc.Request{Action: "catchUp", Target: boot.State.Checkpoint + 1}))
	if e != nil {
		return report, e
	}
	report.Checkpoint = caught.State.Checkpoint
	db, e := sql.Open("sqlite3", store.URI(filepath.Join(cluster.Root, "projection.sqlite"), true))
	if e != nil {
		return report, e
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	if e = equalSQL(ctx, c, db, []model.Item{{ID: 1, Label: "A", Revision: 1}, {ID: 2, Label: "B", Revision: 1}}); e != nil {
		return report, e
	}
	report.Scenarios = append(report.Scenarios, "exported snapshot handoff")
	if e = replayCapture(ctx, boot.State, caught.Capture); e != nil {
		return report, e
	}
	report.Scenarios = append(report.Scenarios, "captured transaction replayed twice")
	// An independent read transaction pins the old WAL-mode SQLite snapshot.
	inspector, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return report, e
	}
	defer inspector.Rollback()
	old, e := projectionRows(ctx, inspector)
	if e != nil {
		return report, e
	}
	if _, e = c.Exec(ctx, "BEGIN; UPDATE public.items SET label='A2',revision=2 WHERE id=1; DELETE FROM public.items WHERE id=2; COMMIT"); e != nil {
		return report, e
	}
	if e = w.send(rpc.Request{Action: "catchUp", Target: caught.State.Checkpoint + 1, Barrier: "commit"}); e != nil {
		return report, e
	}
	if e = w.event(ctx, "commit"); e != nil {
		return report, e
	}
	stillOld, e := projectionRows(ctx, db)
	if e != nil || !reflect.DeepEqual(old, stillOld) {
		return report, fmt.Errorf("uncommitted projection escaped: %v", e)
	}
	src, e := sourceRows(ctx, c)
	if e != nil || !reflect.DeepEqual(src, []model.Item{{ID: 1, Label: "A2", Revision: 2}}) {
		return report, fmt.Errorf("source transaction oracle failed: %v", e)
	}
	if e = w.release(); e != nil {
		return report, e
	}
	atomic, e := requireOK(w.read(ctx))
	if e != nil {
		return report, e
	}
	report.Checkpoint = atomic.State.Checkpoint
	pinned, e := projectionRows(ctx, inspector)
	if e != nil || !reflect.DeepEqual(old, pinned) {
		return report, fmt.Errorf("inspector snapshot changed: %v", e)
	}
	if e = inspector.Commit(); e != nil {
		return report, e
	}
	if e = equalSQL(ctx, c, db, []model.Item{{ID: 1, Label: "A2", Revision: 2}}); e != nil {
		return report, e
	}
	if atomic.State.Applied != caught.State.Applied+1 {
		return report, fmt.Errorf("multi-change transaction counted more than once")
	}
	report.Scenarios = append(report.Scenarios, "atomic update/delete and pinned inspector")
	if _, e = c.Exec(ctx, "BEGIN; INSERT INTO public.items VALUES(9,'rolled back',1); ROLLBACK"); e != nil {
		return report, e
	}
	rolled, e := w.call(ctx, rpc.Request{Action: "catchUp", Target: atomic.State.Checkpoint + 1, TimeoutMS: 250})
	if e != nil {
		return report, e
	}
	if rolled.OK || rolled.Code != "INCOMPLETE" || !unchanged(atomic.State, rolled.State) {
		return report, fmt.Errorf("rollback changed projection/count or idle catch-up claimed success")
	}
	report.Scenarios = append(report.Scenarios, "rollback without marker transaction; explicit incomplete target")
	if _, e = c.Exec(ctx, "INSERT INTO public.items VALUES(3,'C',1)"); e != nil {
		return report, e
	}
	if e = w.send(rpc.Request{Action: "catchUp", Target: atomic.State.Checkpoint + 1, Barrier: "feedback"}); e != nil {
		return report, e
	}
	if e = w.event(ctx, "feedback"); e != nil {
		return report, e
	}
	var durableHex string
	var durableCount int64
	if e = db.QueryRowContext(ctx, "SELECT checkpoint,applied FROM meta WHERE singleton=1").Scan(&durableHex, &durableCount); e != nil {
		return report, e
	}
	durable, e := model.HexLSN(durableHex)
	if e != nil {
		return report, e
	}
	if durable <= atomic.State.Checkpoint || durableCount != atomic.State.Applied+1 {
		return report, fmt.Errorf("feedback barrier preceded durable SQLite commit")
	}
	report.Checkpoint = durable
	if e = equalSQL(ctx, c, db, []model.Item{{ID: 1, Label: "A2", Revision: 2}, {ID: 3, Label: "C", Revision: 1}}); e != nil {
		return report, e
	}
	if e = w.close(); e != nil {
		return report, e
	}
	w = nil
	if _, e = c.Exec(ctx, "INSERT INTO public.items VALUES(4,'D',1)"); e != nil {
		return report, e
	}
	w, e = startWorker(cluster.Root, exe, args)
	if e != nil {
		return report, e
	}
	recovered, e := requireOK(w.call(ctx, rpc.Request{Action: "catchUp", Target: durable + 1}))
	if e != nil {
		return report, e
	}
	report.Checkpoint = recovered.State.Checkpoint
	if recovered.State.Applied != durableCount+1 || recovered.Duplicates < 1 {
		return report, fmt.Errorf("lost feedback did not replay idempotently")
	}
	if e = equalSQL(ctx, c, db, []model.Item{{ID: 1, Label: "A2", Revision: 2}, {ID: 3, Label: "C", Revision: 1}, {ID: 4, Label: "D", Revision: 1}}); e != nil {
		return report, e
	}
	report.Scenarios = append(report.Scenarios, "process kill after commit before feedback; duplicate endpoint ignored")
	if _, e = c.Exec(ctx, "BEGIN; TRUNCATE public.items; INSERT INTO public.items VALUES(1,'reset',1); COMMIT"); e != nil {
		return report, e
	}
	truncated, e := requireOK(w.call(ctx, rpc.Request{Action: "catchUp", Target: recovered.State.Checkpoint + 1}))
	if e != nil {
		return report, e
	}
	report.Checkpoint = truncated.State.Checkpoint
	if e = equalSQL(ctx, c, db, []model.Item{{ID: 1, Label: "reset", Revision: 1}}); e != nil {
		return report, e
	}
	report.Scenarios = append(report.Scenarios, "transactional truncate and insert")
	tx, e := c.Begin(ctx)
	if e != nil {
		return report, e
	}
	for i := 0; i < 65; i++ {
		if _, e = tx.Exec(ctx, "UPDATE public.items SET revision=revision+1 WHERE id=1"); e != nil {
			tx.Rollback(ctx)
			return report, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return report, e
	}
	limited, e := w.call(ctx, rpc.Request{Action: "catchUp", Target: truncated.State.Checkpoint + 1})
	if e != nil {
		return report, e
	}
	if limited.OK || !limited.Incomplete || limited.Code != "TRANSACTION_LIMIT" || !unchanged(truncated.State, limited.State) {
		return report, fmt.Errorf("oversized transaction was not rejected atomically")
	}
	report.Scenarios = append(report.Scenarios, "65-change transaction rejected without checkpoint advance")
	if _, e = requireOK(w.call(ctx, rpc.Request{Action: "stop"})); e != nil {
		return report, e
	}
	if e = w.close(); e != nil {
		return report, e
	}
	w = nil
	if _, e = c.Exec(ctx, "SELECT pg_drop_replication_slot('lab_slot')"); e != nil {
		return report, e
	}
	w, e = startWorker(cluster.Root, exe, args)
	if e != nil {
		return report, e
	}
	missing, e := w.call(ctx, rpc.Request{Action: "catchUp", Target: truncated.State.Checkpoint + 1})
	if e != nil {
		return report, e
	}
	if missing.OK || missing.Code != "REBOOTSTRAP_REQUIRED" || !unchanged(truncated.State, missing.State) {
		return report, fmt.Errorf("missing slot silently repaired or projection changed")
	}
	report.Scenarios = append(report.Scenarios, "missing slot requires explicit rebootstrap, preserving projection")
	report.Checkpoint = missing.State.Checkpoint
	return report, nil
}
