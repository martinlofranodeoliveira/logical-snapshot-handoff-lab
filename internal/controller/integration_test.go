package controller

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/rpc"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/source"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/store"
)

func TestWorkerProcess(t *testing.T) {
	if os.Getenv("LSH_WORKER") != "1" {
		return
	}
	if e := rpc.Serve(); e != nil {
		fmt.Fprintln(os.Stderr, model.Diagnostic(e))
		os.Exit(1)
	}
	os.Exit(0)
}

func TestPrivatePostgresScenarios(t *testing.T) {
	if !source.Available() {
		if os.Getenv("LAB_INTEGRATION") == "1" {
			t.Fatal("LAB_INTEGRATION=1 requires nonroot UID and /usr/lib/postgresql/17/bin")
		}
		t.Skip("private PG17 integration unavailable; set LAB_INTEGRATION=1 to forbid skipping")
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	report, e := Run(ctx, exe, []string{"-test.run=^TestWorkerProcess$"})
	if e != nil {
		t.Fatalf("%v; completed=%v", e, report.Scenarios)
	}
	if len(report.Scenarios) != 8 || report.Incomplete || report.SQLiteSourceID == "" || report.Checkpoint == 0 {
		t.Fatalf("incomplete report: %+v", report)
	}
	t.Logf("linked SQLite %s %s", report.SQLiteVersion, report.SQLiteSourceID)
}

func TestPrivatePostgresBoundaryFailures(t *testing.T) {
	if !source.Available() {
		if os.Getenv("LAB_INTEGRATION") == "1" {
			t.Fatal("LAB_INTEGRATION=1 requires nonroot UID and /usr/lib/postgresql/17/bin")
		}
		t.Skip("private PG17 integration unavailable; set LAB_INTEGRATION=1 to forbid skipping")
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cluster, e := source.Start(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := cluster.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	c, e := source.Connect(ctx, cluster.Root)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	w, e := startWorker(cluster.Root, exe, []string{"-test.run=^TestWorkerProcess$"})
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := w.close(); err != nil {
			t.Fatal(err)
		}
	}()
	boot, e := requireOK(w.call(ctx, rpc.Request{Action: "bootstrap"}))
	if e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", store.URI(filepath.Join(cluster.Root, "projection.sqlite"), true))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)

	// 1. Unreleased barrier deadline reports INCOMPLETE and preserves prior SQLite image.
	if e = w.send(rpc.Request{Action: "bootstrap", Barrier: "snapshot", TimeoutMS: 150}); e != nil {
		t.Fatal(e)
	}
	if e = w.event(ctx, "snapshot"); e != nil {
		t.Fatal(e)
	}
	deadlineRes, e := w.read(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if deadlineRes.OK || !deadlineRes.Incomplete || deadlineRes.Code != "INCOMPLETE" || !unchanged(boot.State, deadlineRes.State) {
		t.Fatalf("expected INCOMPLETE preserving state, got %+v", deadlineRes)
	}

	// 2. Oversized 33-row bootstrap fails closed with TRANSACTION_LIMIT and preserves prior image.
	if _, e = c.Exec(ctx, "INSERT INTO public.items SELECT g, 'row-'||g, 1 FROM generate_series(2, 33) AS g"); e != nil {
		t.Fatal(e)
	}
	overBoot, e := w.call(ctx, rpc.Request{Action: "bootstrap"})
	if e != nil {
		t.Fatal(e)
	}
	if overBoot.OK || !overBoot.Incomplete || overBoot.Code != "TRANSACTION_LIMIT" || !unchanged(boot.State, overBoot.State) {
		t.Fatalf("expected bootstrap TRANSACTION_LIMIT preserving state, got %+v", overBoot)
	}

	// 3. Oversized 33-row replay fails closed with TRANSACTION_LIMIT without advancing checkpoint.
	if _, e = c.Exec(ctx, "DELETE FROM public.items WHERE id > 1"); e != nil {
		t.Fatal(e)
	}
	boot, e = requireOK(w.call(ctx, rpc.Request{Action: "bootstrap"}))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Exec(ctx, "INSERT INTO public.items SELECT g, 'row-'||g, 1 FROM generate_series(2, 33) AS g"); e != nil {
		t.Fatal(e)
	}
	overReplay, e := w.call(ctx, rpc.Request{Action: "catchUp", Target: boot.State.Checkpoint + 1})
	if e != nil {
		t.Fatal(e)
	}
	if overReplay.OK || !overReplay.Incomplete || overReplay.Code != "TRANSACTION_LIMIT" || !unchanged(boot.State, overReplay.State) {
		t.Fatalf("expected replay TRANSACTION_LIMIT preserving state, got %+v", overReplay)
	}

	// 4. Primary-key mutation fails closed with PRIMARY_KEY_MUTATION without advancing checkpoint.
	if _, e = c.Exec(ctx, "DELETE FROM public.items WHERE id > 1"); e != nil {
		t.Fatal(e)
	}
	boot, e = requireOK(w.call(ctx, rpc.Request{Action: "bootstrap"}))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Exec(ctx, "UPDATE public.items SET id = 100 WHERE id = 1"); e != nil {
		t.Fatal(e)
	}
	pkMut, e := w.call(ctx, rpc.Request{Action: "catchUp", Target: boot.State.Checkpoint + 1})
	if e != nil {
		t.Fatal(e)
	}
	if pkMut.OK || !pkMut.Incomplete || pkMut.Code != "PRIMARY_KEY_MUTATION" || !unchanged(boot.State, pkMut.State) {
		t.Fatalf("expected PRIMARY_KEY_MUTATION preserving state, got %+v", pkMut)
	}

	// 5. Explicit rebootstrap after repairing source recovers replication and reaches SQL equality.
	if _, e = c.Exec(ctx, "UPDATE public.items SET id = 1, label = 'recovered', revision = 2 WHERE id = 100"); e != nil {
		t.Fatal(e)
	}
	reboot, e := requireOK(w.call(ctx, rpc.Request{Action: "bootstrap"}))
	if e != nil {
		t.Fatal(e)
	}
	if reboot.State.Applied != 0 || len(reboot.State.Rows) != 1 || reboot.State.Rows[0].Label != "recovered" {
		t.Fatalf("unexpected rebootstrap state: %+v", reboot.State)
	}
	if _, e = c.Exec(ctx, "INSERT INTO public.items VALUES (2, 'steady', 1)"); e != nil {
		t.Fatal(e)
	}
	caught, e := requireOK(w.call(ctx, rpc.Request{Action: "catchUp", Target: reboot.State.Checkpoint + 1}))
	if e != nil {
		t.Fatal(e)
	}
	if caught.State.Applied != 1 {
		t.Fatalf("expected 1 applied transaction after rebootstrap, got %d", caught.State.Applied)
	}
	if e = equalSQL(ctx, c, db, []model.Item{{ID: 1, Label: "recovered", Revision: 2}, {ID: 2, Label: "steady", Revision: 1}}); e != nil {
		t.Fatal(e)
	}

	// 6. Altering source relation layout fails closed with RELATION_LAYOUT and preserves projection.
	if _, e = c.Exec(ctx, "ALTER TABLE public.items ADD COLUMN extra integer NOT NULL DEFAULT 0"); e != nil {
		t.Fatal(e)
	}
	layoutCatch, e := w.call(ctx, rpc.Request{Action: "catchUp", Target: caught.State.Checkpoint + 1})
	if e != nil {
		t.Fatal(e)
	}
	if layoutCatch.OK || !layoutCatch.Incomplete || layoutCatch.Code != "RELATION_LAYOUT" || !unchanged(caught.State, layoutCatch.State) {
		t.Fatalf("expected catchUp RELATION_LAYOUT preserving state, got %+v", layoutCatch)
	}
	layoutBoot, e := w.call(ctx, rpc.Request{Action: "bootstrap"})
	if e != nil {
		t.Fatal(e)
	}
	if layoutBoot.OK || !layoutBoot.Incomplete || layoutBoot.Code != "RELATION_LAYOUT" || !unchanged(caught.State, layoutBoot.State) {
		t.Fatalf("expected bootstrap RELATION_LAYOUT preserving state, got %+v", layoutBoot)
	}
}
