package snowlumaruntime

import (
	"context"
	"sync"
)

// installationStatus mirrors the raw transport event state of one installation.
// It is not a registry lookup: retained callbacks never borrow a replacement.
// Normalization and source-required repeated reads belong to Runtime.
type installationStatus struct {
	mu       sync.RWMutex
	value    string
	disposed bool
}

func newInstallationStatus() *installationStatus { return &installationStatus{value: "disconnected"} }
func (s *installationStatus) observe(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.disposed {
		s.value = value
	}
}
func (s *installationStatus) dispose() {
	s.mu.Lock()
	s.disposed = true
	s.value = "disposed"
	s.mu.Unlock()
}
func (s *installationStatus) read(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.RLock()
	value := s.value
	s.mu.RUnlock()
	return value, nil
}
