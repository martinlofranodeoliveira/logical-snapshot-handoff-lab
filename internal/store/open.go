// Package store owns the SQLite writer fence and durable projection.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
	sqlite3 "github.com/mattn/go-sqlite3"
)

type Store struct {
	db                *sql.DB
	lock              *os.File
	version, sourceID string
}

func URI(path string, readOnly bool) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	if readOnly {
		q.Set("mode", "ro")
	} else {
		q.Set("mode", "rwc")
	}
	q.Set("_busy_timeout", "1000")
	q.Set("_foreign_keys", "on")
	u.RawQuery = q.Encode()
	return u.String()
}
func Open(ctx context.Context, root string) (_ *Store, err error) {
	if !systemSQLite {
		return nil, model.Fail("SQLITE_QUALIFICATION", "build with GOFLAGS=-tags=libsqlite3; bundled SQLite is not admitted")
	}
	s := &Store{}
	s.lock, err = os.OpenFile(filepath.Join(root, "writer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		s.lock.Close()
		return nil, model.Fail("WRITER_BUSY", "another writer holds the projection fence")
	}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	s.db, err = sql.Open("sqlite3", URI(filepath.Join(root, "projection.sqlite"), false))
	if err != nil {
		return nil, err
	}
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	var version, source string
	if err = s.db.QueryRowContext(ctx, "SELECT sqlite_version(), sqlite_source_id()").Scan(&version, &source); err != nil {
		return nil, err
	}
	linked, number, linkedID := sqlite3.Version()
	if number < 3051003 || version != linked || source != linkedID || len(source) < 80 || len(source) > 160 {
		return nil, model.Fail("SQLITE_QUALIFICATION", "external SQLite >=3.51.3 with matching SQL/library version and source ID required")
	}
	s.version, s.sourceID = version, source
	var mode string
	if err = s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return nil, err
	}
	if mode != "wal" {
		return nil, model.Fail("SQLITE_QUALIFICATION", "WAL mode unavailable")
	}
	_, err = s.db.ExecContext(ctx, `PRAGMA synchronous=FULL; PRAGMA wal_autocheckpoint=64; PRAGMA journal_size_limit=1048576;
 CREATE TABLE IF NOT EXISTS items(id INTEGER PRIMARY KEY,label TEXT NOT NULL CHECK(typeof(label)='text' AND length(CAST(label AS BLOB))<=256),revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision>0));
 CREATE TABLE IF NOT EXISTS meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1), identity TEXT NOT NULL CHECK(typeof(identity)='text'), checkpoint TEXT NOT NULL CHECK(typeof(checkpoint)='text' AND length(checkpoint)=16), applied INTEGER NOT NULL CHECK(typeof(applied)='integer' AND applied>=0));
 PRAGMA user_version=1;`)
	if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		if e := s.lock.Close(); err == nil {
			err = e
		}
	}
	return err
}
func readMeta(ctx context.Context, tx *sql.Tx) (model.State, error) {
	st := model.State{Rows: []model.Item{}}
	var id, cp string
	err := tx.QueryRowContext(ctx, "SELECT identity,checkpoint,applied FROM meta WHERE singleton=1").Scan(&id, &cp, &st.Applied)
	if err == sql.ErrNoRows {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err = json.Unmarshal([]byte(id), &st.Identity); err != nil {
		return st, model.Fail("REBOOTSTRAP_REQUIRED", "invalid identity metadata")
	}
	st.Checkpoint, err = model.HexLSN(cp)
	if err != nil {
		return st, err
	}
	if st.Checkpoint == 0 || st.Identity.System == "" || st.Identity.Database == 0 || st.Identity.Table == 0 || st.Identity.Publication != "lab_pub" || st.Identity.Slot != "lab_slot" || st.Applied < 0 {
		return st, model.Fail("REBOOTSTRAP_REQUIRED", "incomplete checkpoint metadata")
	}
	st.Initialized = true
	return st, nil
}
func (s *Store) Snapshot(ctx context.Context) (model.State, error) {
	tx, e := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return model.State{}, e
	}
	defer tx.Rollback()
	st, e := readMeta(ctx, tx)
	if e != nil {
		return st, e
	}
	rows, e := tx.QueryContext(ctx, "SELECT id,label,revision FROM items ORDER BY id LIMIT 33")
	if e != nil {
		return st, e
	}
	for rows.Next() {
		var r model.Item
		if e = rows.Scan(&r.ID, &r.Label, &r.Revision); e != nil {
			rows.Close()
			return st, e
		}
		if e = r.Validate(); e != nil {
			rows.Close()
			return st, model.Fail("REBOOTSTRAP_REQUIRED", "invalid projection row")
		}
		st.Rows = append(st.Rows, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return st, e
	}
	if len(st.Rows) > model.MaxRows || (!st.Initialized && len(st.Rows) != 0) {
		return st, model.Fail("REBOOTSTRAP_REQUIRED", "projection and checkpoint are inconsistent")
	}
	st.SQLiteVersion, st.SQLiteSourceID = s.version, s.sourceID
	if e = tx.Commit(); e != nil {
		return st, fmt.Errorf("read snapshot: %w", e)
	}
	return st, nil
}
