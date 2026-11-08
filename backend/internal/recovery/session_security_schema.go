package recovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var ErrSecuritySchemaUnsupported = errors.New("secret_security_schema_unsupported")

const securityStateDDL = `CREATE TABLE IF NOT EXISTS recovery_security_state (id INTEGER PRIMARY KEY CHECK(id=1), revision TEXT NOT NULL)`

// ensureSecurityStateSchema is a coordinator-only capability. It never accepts
// caller SQL, invents a revision, or invokes callbacks while DDL is admitted.
// The table creation belongs to the existing main transaction and rolls back
// with that transaction. Existing incompatible objects are never repaired.
func ensureSecurityStateSchema(ctx context.Context, conn *sql.Conn) (err error) {
	var kind, definition string
	err = conn.QueryRowContext(ctx, "SELECT type,sql FROM main.sqlite_master WHERE name='recovery_security_state'").Scan(&kind, &definition)
	if err == nil {
		normalized := strings.Replace(strings.ToLower(strings.Join(strings.Fields(definition), "")), "ifnotexists", "", 1)
		if kind != "table" || normalized != "createtablerecovery_security_state(idintegerprimarykeycheck(id=1),revisiontextnotnull)" {
			return ErrSecuritySchemaUnsupported
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = protectMutation(conn, false); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, protectMutation(conn, true)) }()
	_, err = conn.ExecContext(ctx, securityStateDDL)
	return err
}

// EnsureSecurityStateSchema performs the exact source ensure before a security
// intent. It is not a general schema or raw-connection capability.
func (s *WriteSession) EnsureSecurityStateSchema() (err error) {
	if err = s.AssertReady(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.protect(0)) }()
	return s.after(ensureSecurityStateSchema(s.ctx, s.conn))
}
