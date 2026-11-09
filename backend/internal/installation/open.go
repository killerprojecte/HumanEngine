// Package installation composes writable store startup before Engine admission.
package installation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"github.com/killerprojecte/HumanEngine/backend/internal/vectorasset"
	"github.com/mattn/go-sqlite3"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

type Options struct {
	Database           string
	AuthorityDirectory string
	VectorExtension    string
	SourceTimezone     string
}
type Store struct {
	DB            *sql.DB
	Authority     *recovery.Authority
	Secrets       *credentials.Store
	VectorVersion string
	vectorAsset   *vectorasset.Asset
	writerLease   *writerLease
	closeOnce     sync.Once
	closeErr      error
}

// Close is called by the application only after physical owner/request drain.
// A failed drain must not call this release boundary.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if s.Authority != nil {
			s.closeErr = errors.Join(s.closeErr, s.Authority.Close())
		}
		if s.DB != nil {
			s.closeErr = errors.Join(s.closeErr, s.DB.Close())
		}
		if s.vectorAsset != nil {
			s.closeErr = errors.Join(s.closeErr, s.vectorAsset.Close())
		}
		if s.writerLease != nil {
			s.closeErr = errors.Join(s.closeErr, s.writerLease.Close())
		}
	})
	return s.closeErr
}

type connector struct {
	dsn string
	d   *sqlite3.SQLiteDriver
}

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.d.Open(c.dsn)
}
func (c connector) Driver() driver.Driver { return c.d }

// Open takes an explicit genuine extension path; OpenBundled owns an embedded one.
func Open(ctx context.Context, o Options) (*Store, error) { return open(ctx, o, nil) }
func open(ctx context.Context, o Options, asset *vectorasset.Asset) (_ *Store, err error) {
	if err = storage.RequireFTS5(); err != nil {
		return nil, err
	}
	if o.Database == "" || o.Database == ":memory:" {
		return nil, errors.New("installation_file_database_required")
	}
	parser, err := jsdate.New(o.SourceTimezone)
	if err != nil {
		return nil, err
	}
	if asset == nil && !filepath.IsAbs(o.VectorExtension) {
		return nil, errors.New("installation_sqlite_vec_extension_required")
	}
	path, err := filepath.Abs(o.Database)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lease, err := acquireDatabaseLease(path)
	if err != nil {
		return nil, err
	}
	// Until transfer, all constructed resources close before the lifetime lease.
	defer func() {
		if err != nil {
			err = errors.Join(err, lease.Close())
		}
	}()
	loadVector := func(c *sqlite3.SQLiteConn) error {
		if asset != nil {
			return asset.Use(func(path string) error { return c.LoadExtension(path, "sqlite3_vec_init") })
		}
		return c.LoadExtension(o.VectorExtension, "sqlite3_vec_init")
	}
	var phase atomic.Int32
	hook := func(c *sqlite3.SQLiteConn) error {
		if e := c.RegisterFunc("usage_bucket", func(at, zone, bucket string) (string, error) { return usageBucket(parser, at, zone, bucket) }, true); e != nil {
			return e
		}
		if phase.Load() >= 1 {
			if _, e := c.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON", nil); e != nil {
				return e
			}
		}
		if phase.Load() >= 2 {
			return loadVector(c)
		}
		return nil
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "rwc")
	q.Set("_busy_timeout", "5000")
	u.RawQuery = q.Encode()
	db := sql.OpenDB(connector{u.String(), &sqlite3.SQLiteDriver{ConnectHook: hook}})
	// One synchronous handle matches source Store ownership and connection state.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{DB: db, vectorAsset: asset, writerLease: lease}
	defer func() {
		if err != nil {
			if s.Authority != nil {
				err = errors.Join(err, s.Authority.Close())
			}
			err = errors.Join(err, db.Close())
		}
	}()
	if err = db.PingContext(ctx); err != nil {
		return nil, err
	}
	authorityPath, err := recovery.InstallationAuthorityPath(ctx, db, path, o.AuthorityDirectory)
	if err != nil {
		return nil, err
	}
	if err = lease.acquireAuthority(authorityPath); err != nil {
		return nil, err
	}
	s.Authority, err = recovery.OpenInstallationGate(ctx, db, path, o.AuthorityDirectory)
	if err != nil {
		return nil, err
	}
	phase.Store(1)
	if _, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON"); err != nil {
		return nil, err
	}
	s.Secrets, err = credentials.NewStore(ctx, db, s.Authority, filepath.Join(filepath.Dir(path), ".secrets.key"))
	if err != nil {
		return nil, err
	}
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	err = c.Raw(func(raw any) error {
		conn, ok := raw.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("installation_sqlite_driver_invalid")
		}
		return loadVector(conn)
	})
	err = errors.Join(err, c.Close())
	if err != nil {
		return nil, fmt.Errorf("installation sqlite-vec: %w", err)
	}
	phase.Store(2)
	if err = db.QueryRowContext(ctx, "SELECT vec_version()").Scan(&s.VectorVersion); err != nil {
		return nil, err
	}
	if asset != nil && s.VectorVersion != "v"+vectorasset.Version {
		return nil, errors.New("sqlite_vec_bundle_version_mismatch")
	}
	if err = InitializeStoreSchema(ctx, db); err != nil {
		return nil, err
	}
	if err = EnsureBaseline(ctx, db, s.Authority); err != nil {
		return nil, err
	}
	return s, nil
}
