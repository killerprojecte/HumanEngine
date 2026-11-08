package memoryservice

import (
	"database/sql"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// CheckLearningBinding prevents a source owner from reading one database while
// committing candidates through a service belonging to another authority.
func (s *Service) CheckLearningBinding(authority *recovery.Authority, main *sql.DB) error {
	return s.d.Assertions.CheckReadBinding(authority, main, nil)
}
