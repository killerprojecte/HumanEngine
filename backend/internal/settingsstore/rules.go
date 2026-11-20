package settingsstore

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// SetRules uses the configuration row already read by planner turns. It does
// not produce a recovery intent or trigger the settings-change callback.
func (s *Store) SetRules(ctx context.Context, canonical string) error {
	_, err := s.Authority.PrepareWrite(ctx, s.Main, func(read *recovery.MainRead) (recovery.WritePlan, error) {
		if _, err := configValue(read, "rules", nil); err != nil {
			return recovery.WritePlan{}, err
		}
		return recovery.WritePlan{MinimumReader: 1, Apply: func(tx *recovery.MainMutation) error {
			_, err := tx.Exec("INSERT INTO config(key,value) VALUES('rules',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", canonical)
			return err
		}}, nil
	})
	return err
}
