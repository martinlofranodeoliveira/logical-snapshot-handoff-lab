// Package source creates and owns one private PostgreSQL 17 instance.
// There is intentionally no endpoint or connection-string option.
package source

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/fixture"
)

const BinDir = "/usr/lib/postgresql/17/bin"

type BoundedLog struct {
	mu sync.Mutex
	b  []byte
}

func (b *BoundedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := 16*1024 - len(b.b)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.b = append(b.b, p...)
	}
	return n, nil
}
func (b *BoundedLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.ToValidUTF8(b.b, []byte("?")))
}

type Cluster struct {
	Root   string
	cmd    *exec.Cmd
	done   chan error
	log    BoundedLog
	closed bool
}

func Available() bool {
	_, e := os.Stat(filepath.Join(BinDir, "postgres"))
	return e == nil && os.Geteuid() != 0
}
func child(cmd *exec.Cmd, log *BoundedLog) {
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
func Start(ctx context.Context) (_ *Cluster, err error) {
	if os.Geteuid() == 0 {
		return nil, fmt.Errorf("private PostgreSQL must run under an unprivileged UID")
	}
	c := &Cluster{}
	c.Root, err = os.MkdirTemp("/tmp", "lsh-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()
	if err = os.Mkdir(filepath.Join(c.Root, "s"), 0700); err != nil {
		return nil, err
	}
	init := exec.CommandContext(ctx, filepath.Join(BinDir, "initdb"), "-D", filepath.Join(c.Root, "d"), "-U", "lab", "--auth-local=trust", "--auth-host=reject", "--encoding=UTF8", "--no-locale")
	child(init, &c.log)
	init.WaitDelay = 2 * time.Second
	if err = init.Run(); err != nil {
		return nil, fmt.Errorf("initdb failed: %w: %s", err, c.log.String())
	}
	args := []string{"-D", filepath.Join(c.Root, "d"), "-c", "listen_addresses=", "-c", "unix_socket_directories=" + filepath.Join(c.Root, "s"), "-c", "unix_socket_permissions=0700", "-c", "port=55432", "-c", "wal_level=logical", "-c", "max_replication_slots=1", "-c", "max_wal_senders=2", "-c", "max_connections=8", "-c", "shared_buffers=16MB", "-c", "work_mem=1MB", "-c", "maintenance_work_mem=16MB", "-c", "min_wal_size=32MB", "-c", "max_wal_size=64MB", "-c", "max_slot_wal_keep_size=64MB", "-c", "fsync=on", "-c", "synchronous_commit=on", "-c", "full_page_writes=on", "-c", "logging_collector=off"}
	c.cmd = exec.Command(filepath.Join(BinDir, "postgres"), args...)
	child(c.cmd, &c.log)
	c.cmd.WaitDelay = 2 * time.Second
	if err = c.cmd.Start(); err != nil {
		c.cmd = nil
		return nil, err
	}
	c.done = make(chan error, 1)
	go func() { c.done <- c.cmd.Wait() }()
	wait, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for {
		db, e := connectAdmin(wait, c.Root)
		if e == nil {
			var version int
			e = db.QueryRow(wait, "SELECT current_setting('server_version_num')::int").Scan(&version)
			if e == nil && version/10000 != 17 {
				e = fmt.Errorf("PostgreSQL 17 required")
			}
			if e == nil {
				_, e = db.Exec(wait, "CREATE DATABASE lab")
			}
			db.Close(context.Background())
			if e == nil {
				db, e = Connect(wait, c.Root)
				if e == nil {
					_, e = db.Exec(wait, fixture.Schema+fixture.Seed)
					db.Close(context.Background())
				}
			}
			if e != nil {
				return nil, e
			}
			return c, nil
		}
		select {
		case e := <-c.done:
			c.done <- e
			return nil, fmt.Errorf("postmaster exited: %v: %s", e, c.log.String())
		case <-wait.Done():
			return nil, fmt.Errorf("postmaster readiness: %w: %s", wait.Err(), c.log.String())
		case <-time.After(30 * time.Millisecond):
		}
	}
}
func (c *Cluster) Close() error {
	if c == nil || c.closed {
		return nil
	}
	c.closed = true
	if c.cmd != nil {
		_ = c.cmd.Process.Signal(syscall.SIGINT)
		select {
		case e := <-c.done:
			if e != nil {
				return fmt.Errorf("postmaster exited abnormally; namespace retained %s: %w", c.Root, e)
			}
		case <-time.After(5 * time.Second):
			_ = c.cmd.Process.Kill()
			select {
			case <-c.done:
				return fmt.Errorf("postmaster required forced shutdown; namespace retained %s", c.Root)
			case <-time.After(5 * time.Second):
				return fmt.Errorf("owned postmaster not reaped; retained %s", c.Root)
			}
		}
	}
	if c.Root != "" {
		return os.RemoveAll(c.Root)
	}
	return nil
}
