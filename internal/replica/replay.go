package replica

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/source"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/store"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/stream"
)

func (r *Replica) CatchUp(ctx context.Context, target model.LSN, hook store.Hook) (err error) {
	defer func() {
		if err == nil {
			return
		}
		var me *model.Error
		if errors.As(err, &me) {
			return
		}
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, os.ErrDeadlineExceeded) {
			err = model.Fail("INCOMPLETE", "deadline/cancellation before requested durable checkpoint")
		}
	}()
	r.Capture = nil
	if target == 0 {
		return model.Fail("INVALID_LSN", "catchUp target LSN must be nonzero")
	}
	st, e := r.Status(ctx)
	if e != nil {
		return e
	}
	if !st.Initialized {
		return model.Fail("REBOOTSTRAP_REQUIRED", "bootstrap must be explicit")
	}
	c, e := source.Connect(ctx, r.root)
	if e != nil {
		return e
	}
	start, e := source.CheckSlot(ctx, c, st.Identity, st.Checkpoint)
	c.Close(context.Background())
	if e != nil {
		return e
	}
	// Validate the slot even if the target is already satisfied.
	if st.Checkpoint >= target {
		return nil
	}
	conn, e := source.Replication(ctx, r.root)
	if e != nil {
		return e
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	e = pglogrepl.StartReplication(ctx, conn, source.Slot, pglogrepl.LSN(start), pglogrepl.StartReplicationOptions{PluginArgs: []string{"proto_version '1'", "publication_names 'lab_pub'", "binary 'false'", "streaming 'false'", "messages 'false'", "two_phase 'false'"}})
	if e != nil {
		return e
	}
	decoder := stream.New(st.Identity.Table)
	feedback := func() error {
		return pglogrepl.SendStandbyStatusUpdate(ctx, conn, pglogrepl.StandbyStatusUpdate{WALWritePosition: pglogrepl.LSN(st.Checkpoint), WALFlushPosition: pglogrepl.LSN(st.Checkpoint), WALApplyPosition: pglogrepl.LSN(st.Checkpoint)})
	}
	for {
		msg, e := conn.ReceiveMessage(ctx)
		if e != nil {
			return e
		}
		switch m := msg.(type) {
		case *pgproto3.CopyData:
			if len(m.Data) < 1 || len(m.Data) > 96*1024 {
				return model.Fail("TRANSACTION_LIMIT", "replication CopyData outside admitted bound")
			}
			switch m.Data[0] {
			case pglogrepl.PrimaryKeepaliveMessageByteID:
				keep, e := pglogrepl.ParsePrimaryKeepaliveMessage(m.Data[1:])
				if e != nil {
					return model.Fail("INVALID_PROTOCOL", "invalid keepalive")
				}
				if keep.ReplyRequested {
					if e = feedback(); e != nil {
						return e
					}
				}
			case pglogrepl.XLogDataByteID:
				if len(m.Data) < 26 {
					return model.Fail("INVALID_PROTOCOL", "short XLogData")
				}
				x, e := pglogrepl.ParseXLogData(m.Data[1:])
				if e != nil {
					return e
				}
				// Neither x.WALStart nor x.ServerWALEnd participates in checkpointing.
				tx, raw, e := decoder.Feed(x.WALData)
				if e != nil {
					return e
				}
				if tx == nil {
					continue
				}
				applied, e := r.store.Apply(ctx, *tx, hook)
				if e != nil {
					return e
				}
				if applied {
					st.Checkpoint = tx.End
					r.Capture = raw
					if hook != nil {
						if e = hook("feedback"); e != nil {
							return e
						}
					}
				} else {
					r.Duplicates++
				}
				if e = feedback(); e != nil {
					return e
				}
				if st.Checkpoint >= target {
					return nil
				}
			default:
				return model.Fail("UNSUPPORTED_MESSAGE", "unexpected replication CopyData tag")
			}
		case *pgproto3.ErrorResponse:
			return model.Fail("SOURCE_ERROR", "PostgreSQL terminated replication")
		case *pgproto3.NoticeResponse:
		default:
			return model.Fail("INVALID_PROTOCOL", "unexpected replication backend message")
		}
	}
}
