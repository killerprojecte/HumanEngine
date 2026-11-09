package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/mattn/go-sqlite3"
)

// ConnectionInitializer runs once per physical connection, before it enters the
// pool. The callback must not retain the connection. Registered SQL functions
// belong to SQLiteConn and are released by its physical Close, including when
// this initializer fails. No driver name or connection is globally registered.
type ConnectionInitializer func(*sqlite3.SQLiteConn) error

type initializedConnector struct {
	dsn    string
	driver *sqlite3.SQLiteDriver
}

func (c initializedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	conn, e := c.driver.Open(c.dsn)
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		_ = conn.Close()
		return nil, e
	}
	return conn, nil
}
func (c initializedConnector) Driver() driver.Driver { return c.driver }

// OpenExistingInitialized preserves OpenExisting's existing-file mode and
// timeout policy, with a pool-private physical connection initializer.
func OpenExistingInitialized(path string, writable bool, initialize ConnectionInitializer) (*sql.DB, error) {
	if initialize == nil {
		return OpenExisting(path, writable)
	}
	if e := RequireFTS5(); e != nil {
		return nil, e
	}
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	mode := "ro"
	if writable {
		mode = "rw"
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := u.Query()
	q.Set("mode", mode)
	q.Set("_busy_timeout", "5000")
	u.RawQuery = q.Encode()
	db := sql.OpenDB(initializedConnector{dsn: u.String(), driver: &sqlite3.SQLiteDriver{ConnectHook: initialize}})
	if e = db.PingContext(context.Background()); e != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open SQLite: %w", e)
	}
	return db, nil
}
