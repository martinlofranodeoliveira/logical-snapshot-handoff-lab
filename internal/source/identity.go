package source

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
)

const Slot = "lab_slot"
const Publication = "lab_pub"

func Identity(ctx context.Context, c *pgx.Conn) (model.Identity, error) {
	id := model.Identity{Publication: Publication, Slot: Slot}
	var tableOID *uint32
	e := c.QueryRow(ctx, `SELECT (pg_control_system()).system_identifier::text,(SELECT oid FROM pg_database WHERE datname=current_database()),to_regclass('public.items')::oid`).Scan(&id.System, &id.Database, &tableOID)
	if e != nil {
		return id, e
	}
	if tableOID == nil || *tableOID == 0 {
		return id, model.Fail("REBOOTSTRAP_REQUIRED", "items table missing")
	}
	id.Table = *tableOID
	var valid bool
	e = c.QueryRow(ctx, `SELECT c.relkind='r' AND NOT c.relispartition AND c.relreplident='d'
 AND (SELECT count(*) FROM pg_attribute WHERE attrelid=c.oid AND attnum>0 AND NOT attisdropped)=3
 AND (SELECT count(*) FROM pg_index WHERE indrelid=c.oid AND indisprimary AND indnkeyatts=1 AND indkey::text='1')=1
 FROM pg_class c WHERE c.oid=$1`, id.Table).Scan(&valid)
	if e != nil || !valid {
		return id, model.Fail("RELATION_LAYOUT", "items table layout or replica identity changed")
	}
	rows, e := c.Query(ctx, "SELECT attname,atttypid,atttypmod,attnotnull FROM pg_attribute WHERE attrelid=$1 AND attnum>0 AND NOT attisdropped ORDER BY attnum", id.Table)
	if e != nil {
		return id, e
	}
	names := []string{"id", "label", "revision"}
	types := []uint32{20, 25, 20}
	i := 0
	for rows.Next() {
		var name string
		var typ uint32
		var mod int32
		var nn bool
		if e = rows.Scan(&name, &typ, &mod, &nn); e != nil {
			rows.Close()
			return id, e
		}
		if i >= 3 || name != names[i] || typ != types[i] || mod != -1 || !nn {
			rows.Close()
			return id, model.Fail("RELATION_LAYOUT", "unexpected source columns")
		}
		i++
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return id, e
	}
	if i != 3 {
		return id, model.Fail("RELATION_LAYOUT", "missing source columns")
	}
	e = c.QueryRow(ctx, `SELECT COALESCE((SELECT NOT puballtables AND pubinsert AND pubupdate AND pubdelete AND pubtruncate AND NOT pubviaroot
 AND (SELECT count(*) FROM pg_publication_rel WHERE prpubid=p.oid)=1
 AND EXISTS(SELECT 1 FROM pg_publication_rel WHERE prpubid=p.oid AND prrelid=$1 AND prqual IS NULL AND prattrs IS NULL)
 FROM pg_publication p WHERE pubname='lab_pub'), false)`, id.Table).Scan(&valid)
	if e != nil {
		return id, e
	}
	if !valid {
		return id, model.Fail("REBOOTSTRAP_REQUIRED", "publication changed")
	}
	return id, nil
}

// CheckSlot never creates or repairs a slot. Returning its confirmed point
// deliberately permits replay of a commit whose feedback was lost.
func CheckSlot(ctx context.Context, c *pgx.Conn, id model.Identity, cp model.LSN) (model.LSN, error) {
	var plugin, db, confirmed, restart, status, reason string
	e := c.QueryRow(ctx, `SELECT plugin,database,COALESCE(confirmed_flush_lsn::text,''),COALESCE(restart_lsn::text,''),COALESCE(wal_status,''),COALESCE(invalidation_reason,'') FROM pg_replication_slots WHERE slot_name='lab_slot'`).Scan(&plugin, &db, &confirmed, &restart, &status, &reason)
	if e != nil || plugin != "pgoutput" || db != "lab" || (status != "reserved" && status != "extended") || reason != "" {
		return 0, model.Fail("REBOOTSTRAP_REQUIRED", "slot missing, invalidated, or incompatible")
	}
	f, e := model.ParseLSN(confirmed)
	if e != nil {
		return 0, model.Fail("REBOOTSTRAP_REQUIRED", "slot has no confirmed point")
	}
	r, e := model.ParseLSN(restart)
	if e != nil || f > cp || r > cp {
		return 0, model.Fail("REBOOTSTRAP_REQUIRED", "slot is ahead of durable projection or lacks history")
	}
	current, e := Identity(ctx, c)
	if e != nil {
		return 0, e
	}
	if current != id {
		return 0, model.Fail("REBOOTSTRAP_REQUIRED", "source identity changed")
	}
	var now string
	if e = c.QueryRow(ctx, "SELECT pg_current_wal_insert_lsn()::text").Scan(&now); e != nil {
		return 0, e
	}
	n, e := model.ParseLSN(now)
	if e != nil || cp > n {
		return 0, model.Fail("REBOOTSTRAP_REQUIRED", "checkpoint is ahead of source")
	}
	return f, nil
}
