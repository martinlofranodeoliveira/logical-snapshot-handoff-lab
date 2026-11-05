package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/rpc"
	"io"
	"os"
	"testing"
	"time"
)

func TestImmediateReplyFixture(t *testing.T) {
	if os.Getenv("LSH_WORKER") != "1" {
		return
	}
	cfg := os.NewFile(5, "cfg")
	if _, err := io.ReadAll(cfg); err != nil {
		os.Exit(2)
	}
	cfg.Close()
	if err := json.NewEncoder(os.Stdout).Encode(rpc.Response{OK: true, Capture: [][]byte{bytes.Repeat([]byte{0x42}, 80000)}}); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestImmediateExitReplyDrainedBeforeWait(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		w, err := startWorker(t.TempDir(), exe, []string{"-test.run=^TestImmediateReplyFixture$"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		response, readErr := w.read(ctx)
		cancel()
		closeErr := w.close()
		if readErr != nil || closeErr != nil || !response.OK || len(response.Capture) != 1 || !bytes.Equal(response.Capture[0], bytes.Repeat([]byte{0x42}, 80000)) {
			t.Fatalf("reply %d truncated: read=%v close=%v", i, readErr, closeErr)
		}
	}
}
