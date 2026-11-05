// Package barrier implements the three fixed, bounded pipe barriers.
package barrier

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

type Pipes struct{ Gate, Events *os.File }

func (p Pipes) Hook(ctx context.Context, selected string) func(string) error {
	var used bool
	return func(point string) error {
		if used || point != selected || selected == "" {
			return nil
		}
		if point != "snapshot" && point != "commit" && point != "feedback" {
			return fmt.Errorf("invalid barrier")
		}
		used = true
		deadline := time.Now().Add(5 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		if e := p.Events.SetWriteDeadline(deadline); e != nil {
			return e
		}
		if _, e := io.WriteString(p.Events, point+"\n"); e != nil {
			return e
		}
		if e := p.Gate.SetReadDeadline(deadline); e != nil {
			return e
		}
		var b [1]byte
		if _, e := io.ReadFull(p.Gate, b[:]); e != nil {
			return e
		}
		if b[0] != '+' {
			return fmt.Errorf("invalid barrier release")
		}
		return nil
	}
}
