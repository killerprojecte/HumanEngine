package recovery

import "errors"

const ExternalCostTableDDL = `CREATE TABLE budget_external_cost_revisions(id TEXT NOT NULL,revision INTEGER NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL,correction_id TEXT UNIQUE,recorded_at TEXT NOT NULL,PRIMARY KEY(id,revision))`

// ExternalCostColumns consumes one fixed inspection inside the owning writer.
// Ordinary caller statements never get schema privileges.
func (s *WriteSession) ExternalCostColumns() (names []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(); err != nil {
		return nil, err
	}
	if err = s.protect(2); err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, s.protect(0), s.check()) }()
	return fixedTableColumns(s.ctx, s.conn, "PRAGMA main.table_xinfo(budget_external_cost_revisions)")
}

// CreateExternalCostTable is the fixed lazy schema step after an import intent.
func (s *WriteSession) CreateExternalCostTable() error { return s.schema(ExternalCostTableDDL) }
