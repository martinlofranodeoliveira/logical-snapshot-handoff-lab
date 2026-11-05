package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/rpc"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/source"
)

type worker struct {
	cmd       *exec.Cmd
	in        io.WriteCloser
	gate      *os.File
	responses chan rpc.Response
	events    chan string
	done      chan error
	log       source.BoundedLog
	closed    bool
}

func startWorker(root, exe string, args []string) (_ *worker, err error) {
	gateR, gateW, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	eventR, eventW, e := os.Pipe()
	if e != nil {
		gateR.Close()
		gateW.Close()
		return nil, e
	}
	cfgR, cfgW, e := os.Pipe()
	if e != nil {
		gateR.Close()
		gateW.Close()
		eventR.Close()
		eventW.Close()
		return nil, e
	}
	w := &worker{gate: gateW, responses: make(chan rpc.Response, 1), events: make(chan string, 1), done: make(chan error, 1)}
	defer func() {
		gateR.Close()
		eventW.Close()
		cfgR.Close()
		cfgW.Close()
		if err != nil {
			eventR.Close()
			gateW.Close()
			if w.in != nil {
				w.in.Close()
			}
		}
	}()
	w.cmd = exec.Command(exe, args...)
	w.cmd.Env = []string{"PATH=/usr/lib/postgresql/17/bin:/usr/local/bin:/usr/bin:/bin", "LANG=C", "LC_ALL=C", "HOME=/tmp", "LSH_WORKER=1"}
	w.cmd.ExtraFiles = []*os.File{gateR, eventW, cfgR}
	w.cmd.Stderr = &w.log
	w.in, e = w.cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	out, e := w.cmd.StdoutPipe()
	if e != nil {
		return nil, e
	}
	if e = w.cmd.Start(); e != nil {
		return nil, e
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		defer close(w.responses)
		defer out.Close()
		s := bufio.NewScanner(out)
		s.Buffer(make([]byte, 4096), 512*1024)
		for s.Scan() {
			var r rpc.Response
			if json.Unmarshal(s.Bytes(), &r) != nil {
				return
			}
			w.responses <- r
		}
	}()
	// StdoutPipe must be drained before Cmd.Wait closes it. The direct-child
	// handle remains safe to signal while waiting for the fixed helper's EOF.
	go func() { <-drained; w.done <- w.cmd.Wait() }()
	go func() {
		defer eventR.Close()
		defer close(w.events)
		s := bufio.NewScanner(eventR)
		s.Buffer(make([]byte, 128), 128)
		for s.Scan() {
			w.events <- s.Text()
		}
	}()
	if e = json.NewEncoder(cfgW).Encode(rpc.Config{Root: root}); e != nil {
		_ = w.close()
		return nil, e
	}
	return w, nil
}
func (w *worker) send(req rpc.Request) error { return json.NewEncoder(w.in).Encode(req) }
func (w *worker) read(ctx context.Context) (rpc.Response, error) {
	select {
	case r, ok := <-w.responses:
		if !ok {
			return r, fmt.Errorf("replicator exited: %s", w.log.String())
		}
		return r, nil
	case <-ctx.Done():
		return rpc.Response{}, ctx.Err()
	}
}
func (w *worker) call(ctx context.Context, req rpc.Request) (rpc.Response, error) {
	if e := w.send(req); e != nil {
		return rpc.Response{}, e
	}
	return w.read(ctx)
}
func (w *worker) event(ctx context.Context, want string) error {
	select {
	case event, ok := <-w.events:
		if !ok || event != want {
			return fmt.Errorf("expected %s barrier, got %q", want, event)
		}
		return nil
	case r, ok := <-w.responses:
		if !ok {
			return fmt.Errorf("replicator exited before %s barrier: %s", want, w.log.String())
		}
		return fmt.Errorf("replicator returned before %s barrier: code=%s error=%s", want, r.Code, r.Error)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (w *worker) release() error { _, e := w.gate.Write([]byte{'+'}); return e }
func (w *worker) close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	w.in.Close()
	w.gate.Close()
	_ = w.cmd.Process.Kill()
	select {
	case <-w.done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("replicator not reaped")
	}
}
func requireOK(r rpc.Response, e error) (rpc.Response, error) {
	if e != nil {
		return r, e
	}
	if !r.OK {
		return r, fmt.Errorf("%s", r.Error)
	}
	return r, nil
}
