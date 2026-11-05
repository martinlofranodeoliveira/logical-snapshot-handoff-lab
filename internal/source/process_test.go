package source

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// Qualify the concrete runtime handle policy after an actual wait, rather
// than a bool updated after wait or a fake numeric-PID supervisor.
func TestReapedDirectChildHandleCannotSignal(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("signal after wait: %v", err)
	}
	if err := cmd.Process.Kill(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill after wait: %v", err)
	}
}

func TestClusterCloseWithAlreadyReapedDirectChild(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "lsh-close-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	done <- nil
	c := &Cluster{Root: root, cmd: cmd, done: done}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("namespace remains after successful cleanup: %v", err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}
