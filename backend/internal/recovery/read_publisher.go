package recovery

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"sync"
)

// ReadPublisher isolates HTTP snapshot checkout and the authority verification
// connection from the application's writer pool/mutex. It does NOT bypass the
// publication fence or treat an old head as reusable authorization. Authority
// changes still invalidate the verification cache and may require a full audit.
type ReadPublisher struct {
	originMain      *sql.DB
	originAuthority *Authority
	main            *sql.DB
	authority       *Authority
	work            quiescence.Group
	finish          sync.Once
	closeErr        error
}

func NewReadPublisher(ctx context.Context, main *sql.DB, authority *Authority) (*ReadPublisher, error) {
	if main == nil || authority == nil {
		return nil, errors.New("recovery_read_owner_required")
	}
	rows, err := main.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return nil, err
	}
	path := ""
	for rows.Next() {
		var seq int
		var name, file string
		if err = rows.Scan(&seq, &name, &file); err != nil {
			break
		}
		if name == "main" {
			path = file
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("recovery_read_disk_owner_required")
	}
	readMain, err := storage.OpenExisting(path, false)
	if err != nil {
		return nil, err
	}
	readAuthority, err := Open(ctx, authority.path)
	if err != nil {
		readMain.Close()
		return nil, err
	}
	r := &ReadPublisher{originMain: main, originAuthority: authority, main: readMain, authority: readAuthority}
	// Verify exact path/gate/head binding before exposure. No successful read
	// permissions survive this transaction; every Publish repeats current checks.
	err = r.Publish(ctx, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
	if err != nil {
		r.CloseAndWait(context.Background())
		return nil, err
	}
	return r, nil
}
func (r *ReadPublisher) OwnedBy(main *sql.DB, authority *Authority) bool {
	return r != nil && r.originMain == main && r.originAuthority == authority
}
func (r *ReadPublisher) Publish(ctx context.Context, read func(*sql.Conn) ([]byte, error), emit func([]byte) error) error {
	done, ok := r.work.Start()
	if !ok {
		return errors.New("recovery_read_publisher_closed")
	}
	defer done()
	return r.authority.Publish(ctx, r.main, read, emit)
}
func (r *ReadPublisher) CloseAndWait(ctx context.Context) error {
	if err := r.work.Wait(ctx); err != nil {
		return err
	}
	r.finish.Do(func() { r.closeErr = errors.Join(r.authority.Close(), r.main.Close()) })
	return r.closeErr
}
