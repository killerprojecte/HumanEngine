package httpapi

import (
	"database/sql"
	"errors"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type ownership struct {
	once  sync.Once
	close func() error
	err   error
}

// New borrows the main pool and authority coordinator. The caller must keep both
// alive until requests finish and must close them itself, including on failure.
// It neither enrolls nor reconciles the database. Publish checks the gate on
// every request, using exactly the same response semantics as the preview.
func New(main *sql.DB, authority *recovery.Authority, token string, logs *observability.Logger) (*Server, error) {
	if main == nil || authority == nil {
		return nil, errors.New("http_dependencies_invalid")
	}
	schemas, err := contract.NewSchemas()
	if err != nil {
		return nil, err
	}
	return &Server{main: main, authority: authority, schemas: schemas, token: token, logs: logs}, nil
}
