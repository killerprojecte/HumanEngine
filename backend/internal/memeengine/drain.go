package memeengine

import "context"

// CloseAndWait is the no-media composition's actual resource barrier. Consumers
// must stop plugin/HTTP producers first. Failed waits retain caller ownership.
func (s *Service) CloseAndWait(ctx context.Context) error {
	if err := s.Runtime.CloseAndWait(ctx); err != nil {
		return err
	}
	return s.Models.CloseAndWait(ctx)
}
