package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var sanitize sync.Once

// pgx's parser has environment defaults. Remove every PG* variable before it
// is called, including service/password-file settings. This lab owns its process.
func cleanEnvironment() {
	sanitize.Do(func() {
		for _, kv := range os.Environ() {
			name, _, _ := strings.Cut(kv, "=")
			if strings.HasPrefix(name, "PG") {
				_ = os.Unsetenv(name)
			}
		}
	})
}
func ValidateRoot(root string) error {
	if filepath.Dir(root) != "/tmp" || !strings.HasPrefix(filepath.Base(root), "lsh-") || strings.ContainsAny(root, "'\\\n\r") {
		return fmt.Errorf("not a private lab root")
	}
	st, e := os.Lstat(root)
	if e != nil {
		return e
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || st.Mode().Perm() != 0700 || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("private root must be owned by this UID with mode 0700")
	}
	return nil
}
func config(root string) (*pgx.ConnConfig, error) {
	if e := ValidateRoot(root); e != nil {
		return nil, e
	}
	cleanEnvironment()
	c, e := pgx.ParseConfig("host='" + filepath.Join(root, "s") + "' port=55432 user=lab dbname=lab sslmode=disable connect_timeout=2 application_name=lsh options='' passfile=/dev/null")
	if e != nil {
		return nil, e
	}
	c.ConnectTimeout = 2 * time.Second
	c.Fallbacks = nil
	c.Password = ""
	return c, nil
}
func Connect(ctx context.Context, root string) (*pgx.Conn, error) {
	c, e := config(root)
	if e != nil {
		return nil, e
	}
	return pgx.ConnectConfig(ctx, c)
}
func Replication(ctx context.Context, root string) (*pgconn.PgConn, error) {
	c, e := config(root)
	if e != nil {
		return nil, e
	}
	c.RuntimeParams["replication"] = "database"
	return pgconn.ConnectConfig(ctx, &c.Config)
}

// Startup alone connects to the fixed maintenance database inside the same
// private Unix socket namespace, before the fixed lab database exists.
func connectAdmin(ctx context.Context, root string) (*pgx.Conn, error) {
	c, e := config(root)
	if e != nil {
		return nil, e
	}
	c.Database = "postgres"
	return pgx.ConnectConfig(ctx, c)
}
