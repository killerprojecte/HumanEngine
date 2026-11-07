package botruntime

import (
	"fmt"
)

func toolNamed(tools []*Object, name string) *Object {
	for _, t := range tools {
		if get(t, "name") == name {
			return t
		}
	}
	return nil
}
func (r *Runtime) stageError(t *turn, s *stage, role string, err error) error {
	if !t.context.root.Aborted() && s.Err() != nil {
		if role == "planner" {
			return fmt.Errorf("planner_timeout: %w", err)
		}
		return fmt.Errorf("dialogue_timeout: %w", err)
	}
	return err
}
