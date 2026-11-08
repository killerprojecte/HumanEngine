// Package candidateservice composes existing candidate stores and HTTP reads.
// It is not installation enrollment, reconciliation, or production startup.
package candidateservice

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/httpapi"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
	"github.com/killerprojecte/HumanEngine/backend/internal/settingsstore"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
)

var ErrClosed = errors.New("candidate_service_closed")

// Service owns its database and authority. Neither store nor handler can close
// them independently. The caller owns listeners and the optional logger.
type Service struct {
	mu        sync.Mutex
	drained   *sync.Cond
	active    int
	closeDone chan struct{}
	closed    bool
	closeErr  error
	main      *sql.DB
	authority *recovery.Authority
	http      *httpapi.Server
	settings  settingsstore.Store
	scenes    scenestore.Store
}

// Open opens an already enrolled installation without changing its schemas or
// repairing ACK gaps. As in the preview, actual publication verifies authority.
func Open(ctx context.Context, database, token string, logs *observability.Logger) (*Service, error) {
	return open(ctx, database, token, logs, false)
}

// OpenWithHabits initializes concrete habit storage and enables its actual admin
// routes. Unlike Open it may initialize that domain schema, but never enrolls or
// reconciles an installation or replays learning.
func OpenWithHabits(ctx context.Context, database, token string, logs *observability.Logger) (*Service, error) {
	return open(ctx, database, token, logs, true)
}
func open(ctx context.Context, database, token string, logs *observability.Logger, withHabits bool) (*Service, error) {
	main, err := storage.OpenExisting(database, true)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Service, error) { return nil, errors.Join(err, main.Close()) }
	var mode string
	if err = main.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fail(err)
	}
	if mode != "wal" {
		return fail(errors.New("recovery_read_journal_unsupported"))
	}
	gate, err := recovery.ReadGate(ctx, main)
	if err != nil {
		return fail(err)
	}
	authority, err := recovery.OpenWriter(ctx, gate.Authority)
	if err != nil {
		return fail(err)
	}
	var handler *httpapi.Server
	if withHabits {
		var habits *habitservice.Service
		habits, err = habitservice.New(ctx, main, authority, habitservice.Options{})
		if err == nil {
			handler, err = httpapi.NewWithHabits(main, authority, habits, token, logs)
		}
	} else {
		handler, err = httpapi.New(main, authority, token, logs)
	}
	if err != nil {
		return fail(errors.Join(err, authority.Close()))
	}
	s := &Service{main: main, authority: authority, http: handler, closeDone: make(chan struct{}),
		settings: settingsstore.Store{Main: main, Authority: authority},
		scenes:   scenestore.Store{Main: main, Authority: authority}}
	s.drained = sync.NewCond(&s.mu)
	return s, nil
}

// ServeHTTP exposes only the routes enabled by the selected constructor.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.admit() {
		s.http.ServeUnavailable(w, r)
		return
	}
	defer s.release()
	s.http.ServeHTTP(w, r)
}

// SetSettings preserves the store's JSON-domain and durable-failure semantics.
// A failed write may leave intent; callers must not automatically retry it.
func (s *Service) SetSettings(ctx context.Context, value any) (recovery.Head, error) {
	if !s.admit() {
		return recovery.Head{}, ErrClosed
	}
	defer s.release()
	return s.settings.Set(ctx, value)
}

// SaveScene requires a schema-validated scene, just like scenestore.Save.
func (s *Service) SaveScene(ctx context.Context, value *jsjson.Object) (recovery.Head, error) {
	if !s.admit() {
		return recovery.Head{}, ErrClosed
	}
	defer s.release()
	return s.scenes.Save(ctx, value)
}

// admit holds the lifecycle lock only for accounting, never during application
// callbacks, authority acquisition, database operations, or response emission.
func (s *Service) admit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.active++
	return true
}

func (s *Service) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	if s.active == 0 {
		s.drained.Broadcast()
	}
}

// Close stops admission immediately, drains active service calls, and closes
// owned resources exactly once. Concurrent Close calls await the same outcome.
// Shut down the caller-owned HTTP server first to stop accepting new requests.
// Request cancellation remains controlled by each request's context. Close must
// not be called reentrantly from a response writer or another active service call.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return s.closeErr
	}
	s.closed = true
	for s.active != 0 {
		s.drained.Wait()
	}
	s.mu.Unlock()
	err := errors.Join(s.authority.Close(), s.main.Close())
	s.mu.Lock()
	s.closeErr = err
	close(s.closeDone)
	s.mu.Unlock()
	return err
}
