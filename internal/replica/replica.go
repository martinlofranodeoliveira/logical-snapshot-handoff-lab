// Package replica exposes Bootstrap, CatchUp, Status and Stop for the owned lab.
package replica

import (
	"context"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/source"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/store"
)

type Replica struct {
	root       string
	store      *store.Store
	Duplicates uint64
	Capture    [][]byte
}

func Open(ctx context.Context, root string) (*Replica, error) {
	if e := source.ValidateRoot(root); e != nil {
		return nil, e
	}
	s, e := store.Open(ctx, root)
	if e != nil {
		return nil, e
	}
	return &Replica{root: root, store: s}, nil
}
func (r *Replica) Status(ctx context.Context) (model.State, error) { return r.store.Snapshot(ctx) }
func (r *Replica) Stop() error                                     { return r.store.Close() }
