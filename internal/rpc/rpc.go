// Package rpc is a bounded JSON-lines control protocol on inherited pipes.
package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/barrier"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/replica"
)

type Config struct {
	Root string `json:"root"`
}
type Request struct {
	Action    string    `json:"action"`
	Target    model.LSN `json:"target"`
	Barrier   string    `json:"barrier"`
	TimeoutMS int       `json:"timeout_ms"`
}
type Response struct {
	OK         bool        `json:"ok"`
	Incomplete bool        `json:"incomplete"`
	Code       string      `json:"code,omitempty"`
	Error      string      `json:"error,omitempty"`
	State      model.State `json:"state"`
	Duplicates uint64      `json:"duplicate_endpoints"`
	Capture    [][]byte    `json:"capture,omitempty"`
}

func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("extra JSON value")
	}
	return nil
}

// Serve is not a network service. Descriptor 5 must contain controller-created
// configuration; descriptors 3/4 are the fixed gate/event pipes.
func Serve() error {
	configFile := os.NewFile(5, "private-config")
	if configFile == nil {
		return fmt.Errorf("missing controller configuration")
	}
	defer configFile.Close()
	b, e := io.ReadAll(io.LimitReader(configFile, 4097))
	if e != nil || len(b) > 4096 {
		return fmt.Errorf("invalid controller configuration")
	}
	var cfg Config
	if e = decode(b, &cfg); e != nil {
		return e
	}
	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	r, e := replica.Open(initCtx, cfg.Root)
	cancel()
	if e != nil {
		return e
	}
	defer r.Stop()
	// ExtraFiles are inherited as blocking descriptors. Register nonblocking
	// pipes before os.NewFile so Go's poller can enforce actual deadlines.
	if e = syscall.SetNonblock(3, true); e != nil {
		return e
	}
	if e = syscall.SetNonblock(4, true); e != nil {
		return e
	}
	pipes := barrier.Pipes{Gate: os.NewFile(3, "gate"), Events: os.NewFile(4, "events")}
	if pipes.Gate == nil || pipes.Events == nil {
		return fmt.Errorf("missing barrier descriptors")
	}
	defer pipes.Gate.Close()
	defer pipes.Events.Close()
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 2048), 2048)
	enc := json.NewEncoder(os.Stdout)
	for scan.Scan() {
		var req Request
		var err error
		if err = decode(scan.Bytes(), &req); err != nil {
			return fmt.Errorf("invalid control request")
		}
		if req.TimeoutMS == 0 {
			req.TimeoutMS = 5000
		}
		if req.TimeoutMS < 1 || req.TimeoutMS > 5000 {
			return fmt.Errorf("timeout must be 1..5000 ms")
		}
		if req.Barrier != "" && req.Barrier != "snapshot" && req.Barrier != "commit" && req.Barrier != "feedback" {
			return fmt.Errorf("unknown barrier")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutMS)*time.Millisecond)
		hook := pipes.Hook(ctx, req.Barrier)
		switch req.Action {
		case "bootstrap":
			if req.Target != 0 || req.Barrier == "feedback" {
				cancel()
				return fmt.Errorf("invalid bootstrap parameters")
			}
			err = r.Bootstrap(ctx, hook)
		case "catchUp":
			if req.Barrier == "snapshot" {
				cancel()
				return fmt.Errorf("invalid catchUp barrier")
			}
			err = r.CatchUp(ctx, req.Target, hook)
		case "status", "stop":
			if req.Target != 0 || req.Barrier != "" {
				cancel()
				return fmt.Errorf("invalid %s parameters", req.Action)
			}
		default:
			cancel()
			return fmt.Errorf("unknown operation")
		}
		cancel()
		statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
		st, statusErr := r.Status(statusCtx)
		statusCancel()
		if err == nil {
			err = statusErr
		}
		res := Response{OK: err == nil, Incomplete: err != nil, State: st, Duplicates: r.Duplicates}
		if err != nil {
			res.Code = model.Code(err)
			res.Error = model.Diagnostic(err)
		} else if req.Action == "catchUp" {
			res.Capture = r.Capture
		}
		if e = enc.Encode(res); e != nil {
			return e
		}
		if req.Action == "stop" {
			return nil
		}
	}
	return scan.Err()
}
