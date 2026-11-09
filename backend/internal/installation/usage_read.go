package installation

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"github.com/mattn/go-sqlite3"
)

// UsageReadInitializer installs the source-compatible function on each physical
// read-publisher connection. It captures only the immutable parser, never a
// connection. The SQLite driver owns callback handles until physical Close.
func UsageReadInitializer(parser *jsdate.Parser) storage.ConnectionInitializer {
	return func(c *sqlite3.SQLiteConn) error {
		if c == nil || parser == nil {
			return errors.New("usage_reader_dependencies_invalid")
		}
		return c.RegisterFunc("usage_bucket", func(at, zone, bucket string) (string, error) { return usageBucket(parser, at, zone, bucket) }, true)
	}
}
