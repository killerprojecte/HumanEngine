package installation

import (
	"errors"
	"os"
	"sync"
)

// ErrWriterBusy is deliberately path- and identity-free.
var ErrWriterBusy = errors.New("installation_writer_busy")
var errWriterLeasePath = errors.New("installation_writer_lease_path_invalid")

// Separate database and authority locks serialize both NEW installation races
// (including different requested authorities) and shared durable authorities.
// Files are never unlinked: replacing a lock inode would split ownership.
type writerLease struct {
	database, authority *os.File
	once                sync.Once
	err                 error
}

func (l *writerLease) Close() error {
	l.once.Do(func() {
		if l.authority != nil {
			l.err = errors.Join(l.err, l.authority.Close())
		}
		if l.database != nil {
			l.err = errors.Join(l.err, l.database.Close())
		}
	})
	return l.err
}
