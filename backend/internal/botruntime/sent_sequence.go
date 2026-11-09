package botruntime

import (
	"context"
	"sync"
	"sync/atomic"
)

// SentSequencer orders publication against subsequent ingress. Reserve performs
// only local bookkeeping. User callbacks run only after Release, outside scene
// ownership. A reservation is never made before acquiring the scene owner.
type SentSequencer struct {
	ctx     context.Context
	run     func(Scene, string, string)
	mu      sync.Mutex
	tail    chan struct{}
	queue   []*SentReservation
	running bool
	closed  bool
}
type SentReservation struct {
	ready        chan struct{}
	once         sync.Once
	canceled     atomic.Bool
	done         chan struct{}
	scene        Scene
	turn, target string
}

func NewSentSequencer(ctx context.Context, run func(Scene, string, string)) *SentSequencer {
	return &SentSequencer{ctx: ctx, run: run}
}
func (s *SentSequencer) reserve(scene Scene, turn, target string) *SentReservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return nil
	}
	reservation := &SentReservation{ready: make(chan struct{}), done: make(chan struct{}), scene: scene, turn: turn, target: target}
	s.tail = reservation.done
	s.queue = append(s.queue, reservation)
	if !s.running {
		s.running = true
		go s.drain()
	}
	return reservation
}
func (s *SentSequencer) drain() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		reservation := s.queue[0]
		s.queue[0] = nil
		s.queue = s.queue[1:]
		s.mu.Unlock()
		s.execute(reservation)
	}
}
func (s *SentSequencer) execute(reservation *SentReservation) {
	defer close(reservation.done)
	// Domain callbacks are responsible for reporting failures. This final
	// containment still ensures a faulty callback cannot strand the queue.
	defer func() { _ = recover() }()
	select {
	case <-s.ctx.Done():
		return
	case <-reservation.ready:
	}
	if !reservation.canceled.Load() && s.ctx.Err() == nil && s.run != nil {
		s.run(reservation.scene, reservation.turn, reservation.target)
	}
}

func (r *SentReservation) Release() {
	if r != nil {
		r.once.Do(func() { close(r.ready) })
	}
}
func (s *SentSequencer) Barrier() error {
	s.mu.Lock()
	tail := s.tail
	s.mu.Unlock()
	if tail == nil {
		return s.ctx.Err()
	}
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-tail:
		return s.ctx.Err()
	}
}

func (r *SentReservation) Cancel() {
	if r != nil {
		r.canceled.Store(true)
		r.Release()
	}
}

// CloseAndWait seals future reservations and joins their actual callback tail.
// Owner cancellation can suppress queued work, but cannot stand in for a running
// callback returning. The caller controls only how long it waits, not ownership.
func (s *SentSequencer) CloseAndWait(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	tail := s.tail
	s.mu.Unlock()
	if tail == nil {
		return nil
	}
	select {
	case <-tail:
		return nil
	default:
	}
	select {
	case <-tail:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
