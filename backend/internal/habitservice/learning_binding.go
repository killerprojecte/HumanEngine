package habitservice

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// CheckLearningBinding verifies that the borrowed live habit command owner uses
// the same root database and authority as the source-learning runtime.
func (s *Service) CheckLearningBinding(authority *recovery.Authority, main *sql.DB) error {
	if s == nil || s.main != main || s.authority != authority || s.session != nil {
		return errors.New("habit_learning_binding")
	}
	return s.bindingError
}
