package botruntime

import (
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
)

// SegmentSentSequencer is the controlled message.sent event queue. Reservation
// clones the payload but never invokes domain code while ACK publication owns a
// scene. Accepted callbacks remain in Runtime's physical shutdown barrier.
type SegmentSentSequencer struct {
	run     func(Scene, string, dispatch.Segment, dispatch.Receipt)
	mu      sync.Mutex
	queue   []*SegmentSentReservation
	running bool
}
type SegmentSentReservation struct {
	runtime   *Runtime
	ready     chan struct{}
	once      sync.Once
	cancelled atomic.Bool
	done      func()
	scene     Scene
	turn      string
	segment   dispatch.Segment
	receipt   dispatch.Receipt
}

func NewSegmentSentSequencer(run func(Scene, string, dispatch.Segment, dispatch.Receipt)) *SegmentSentSequencer {
	return &SegmentSentSequencer{run: run}
}
func (s *SegmentSentSequencer) reserve(runtime *Runtime, scene Scene, turn string, segment dispatch.Segment, receipt dispatch.Receipt) *SegmentSentReservation {
	done, ok := runtime.work.Child()
	if !ok {
		return nil
	}
	parts := make([]json.RawMessage, len(segment.Parts))
	for i, part := range segment.Parts {
		parts[i] = append(json.RawMessage(nil), part...)
	}
	segment.Parts = parts
	if segment.ReplyToID != nil {
		value := *segment.ReplyToID
		segment.ReplyToID = &value
	}
	if receipt.MessageID != nil {
		value := *receipt.MessageID
		receipt.MessageID = &value
	}
	if receipt.Detail != nil {
		value := *receipt.Detail
		receipt.Detail = &value
	}
	reservation := &SegmentSentReservation{runtime: runtime, ready: make(chan struct{}), done: done, scene: scene, turn: turn, segment: segment, receipt: receipt}
	s.mu.Lock()
	s.queue = append(s.queue, reservation)
	if !s.running {
		s.running = true
		go s.drain()
	}
	s.mu.Unlock()
	return reservation
}
func (s *SegmentSentSequencer) drain() {
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
func (s *SegmentSentSequencer) execute(reservation *SegmentSentReservation) {
	runtime := reservation.runtime
	defer reservation.done()
	defer func() {
		if recover() != nil {
			runtime.trace(Event{SceneID: reservation.scene.ID, TurnID: reservation.turn, Stage: "events", Status: "failed", Reason: "listener_failed"})
		}
	}()
	<-reservation.ready
	if reservation.cancelled.Load() || s.run == nil {
		return
	}
	if reservation.receipt.MessageID != nil {
		recalled, e := runtime.services.Authority.IsRecalled(reservation.scene, *reservation.receipt.MessageID)
		if e != nil {
			runtime.trace(Event{SceneID: reservation.scene.ID, TurnID: reservation.turn, Stage: "events", Status: "failed", Reason: "listener_failed"})
			return
		}
		if recalled {
			reservation.segment.Parts = []json.RawMessage{}
		}
	}
	s.run(reservation.scene, reservation.turn, reservation.segment, reservation.receipt)
}
func (r *SegmentSentReservation) Release() {
	if r != nil {
		r.once.Do(func() { close(r.ready) })
	}
}
func (r *SegmentSentReservation) Cancel() {
	if r != nil {
		r.cancelled.Store(true)
		r.Release()
	}
}
