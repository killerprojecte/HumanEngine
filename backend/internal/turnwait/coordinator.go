// Package turnwait owns topic-local admission decisions, not timers, models or
// transport. Its owner serializes calls and supplies the clock. A recheck wake
// never grants permission to call a model or retry a delivery.
package turnwait

import (
	"errors"
	"math"
)

var (
	ErrInvalidConfig = errors.New("invalid_turn_wait_config")
	ErrInvalidEvent  = errors.New("invalid_turn_wait_event")
)

// Config preserves the existing JavaScript numeric contract. Configuration
// values must be nonnegative safe integers; timestamps and WAIT results remain
// numbers so fractional deadlines and nonfinite rejection match the oracle.
// MaxPending is existing caller policy, not a new protective limit.
type Config struct {
	QuietMS          float64 `json:"quietMs"`
	MaxWaitMS        float64 `json:"maxWaitMs"`
	AddressedQuietMS float64 `json:"addressedQuietMs"`
	MaxModelWaitMS   float64 `json:"maxModelWaitMs"`
	MaxPending       float64 `json:"maxPending"`
}

type Event struct {
	ID         string
	Meaningful bool
	Addressed  bool
}

// Wake identity is part of the contract. Finish and IsCurrent require the exact
// pointer returned by TakeReady, not a reconstructed wake with equal fields.
type Wake struct {
	Kind       string
	Generation float64
	EventIDs   []string
	Reason     string
}

type Result struct {
	WaitUntil *float64
}

type Retention struct {
	Active  bool `json:"active"`
	Pending bool `json:"pending"`
	Waiting bool `json:"waiting"`
}

type pendingEvent struct {
	Event
	at float64
}

type waiting struct {
	at         float64
	generation float64
	eventIDs   []string
}

type Coordinator struct {
	config             Config
	now                func() float64
	generation         float64
	pending            map[string]pendingEvent
	pendingOrder       []string
	seen               map[string]struct{}
	seenOrder          []string
	active             *Wake
	waiting            *waiting
	lastWaitGeneration float64
}

func New(config Config, now func() float64) (*Coordinator, error) {
	c := &Coordinator{
		now: now, pending: make(map[string]pendingEvent), seen: make(map[string]struct{}),
		lastWaitGeneration: -1,
	}
	if err := c.Configure(config); err != nil {
		return nil, err
	}
	return c, nil
}

// Configure changes deadlines without discarding input or active-attempt identity.
func (c *Coordinator) Configure(config Config) error {
	for _, value := range []float64{config.QuietMS, config.MaxWaitMS, config.AddressedQuietMS, config.MaxModelWaitMS, config.MaxPending} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 9007199254740991 || math.Trunc(value) != value {
			return ErrInvalidConfig
		}
	}
	if config.MaxPending < 1 || config.MaxWaitMS < config.QuietMS {
		return ErrInvalidConfig
	}
	c.config = config
	return nil
}

func (c *Coordinator) Observe(event Event) (string, error) {
	if event.ID == "" {
		return "", ErrInvalidEvent
	}
	if _, exists := c.seen[event.ID]; exists {
		return "duplicate", nil
	}
	if float64(len(c.pending)) >= c.config.MaxPending {
		return "full", nil
	}
	c.pending[event.ID] = pendingEvent{Event: event, at: c.now()}
	c.pendingOrder = append(c.pendingOrder, event.ID)
	c.seen[event.ID] = struct{}{}
	c.seenOrder = append(c.seenOrder, event.ID)
	// Remove exactly one oldest ID, even after configuration shrinks the policy.
	// Durable platform replay deduplication remains the ingress owner's job.
	if float64(len(c.seen)) > c.config.MaxPending*4 {
		delete(c.seen, c.seenOrder[0])
		c.seenOrder[0] = ""
		c.seenOrder = c.seenOrder[1:]
	}
	c.generation++
	return "accepted", nil
}

func (c *Coordinator) Recall(id string) {
	_, removed := c.pending[id]
	c.removePending(id)
	affectsWait := c.waiting != nil && contains(c.waiting.eventIDs, id)
	if removed || affectsWait || (c.active != nil && contains(c.active.EventIDs, id)) {
		c.generation++
	}
	if affectsWait {
		c.waiting = nil
	}
}

// Replace retires a trusted edit's old snapshot but never releases an active
// attempt or grants a transport retry. The caller observes the replacement.
func (c *Coordinator) Replace(id string) error {
	if id == "" {
		return ErrInvalidEvent
	}
	c.removePending(id)
	delete(c.seen, id)
	c.seenOrder = remove(c.seenOrder, id)
	if c.waiting != nil && contains(c.waiting.eventIDs, id) {
		c.waiting = nil
	}
	c.generation++
	return nil
}

func (c *Coordinator) Retention() Retention {
	return Retention{Active: c.active != nil, Pending: len(c.pending) > 0, Waiting: c.waiting != nil}
}

// RetainedEventIDs returns live references in insertion order, excluding replay IDs.
func (c *Coordinator) RetainedEventIDs() []string {
	ids := make([]string, 0, len(c.pendingOrder))
	seen := make(map[string]struct{})
	add := func(values []string) {
		for _, id := range values {
			if _, exists := seen[id]; !exists {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	add(c.pendingOrder)
	if c.active != nil {
		add(c.active.EventIDs)
	}
	if c.waiting != nil {
		add(c.waiting.eventIDs)
	}
	return ids
}

func (c *Coordinator) IsCurrent(wake *Wake) bool {
	return wake != nil && wake == c.active && wake.Generation == c.generation
}

// PendingAddressed returns (false, false) for a timer-only recheck.
func (c *Coordinator) PendingAddressed() (addressed, present bool) {
	if len(c.pending) == 0 {
		return false, false
	}
	for _, event := range c.pending {
		if event.Addressed {
			return true, true
		}
	}
	return false, true
}

func (c *Coordinator) NextWakeAt() (float64, bool) {
	if c.active != nil {
		return 0, false
	}
	deadline, present := c.pendingDeadline()
	if c.waiting != nil {
		if !present {
			return c.waiting.at, true
		}
		return math.Min(deadline, c.waiting.at), true
	}
	return deadline, present
}

func (c *Coordinator) TakeReady() *Wake {
	if c.active != nil {
		return nil
	}
	now := c.now()
	deadline, present := c.pendingDeadline()
	if !present || deadline > now {
		if c.waiting == nil || c.waiting.at > now {
			return nil
		}
		generation := c.waiting.generation
		c.waiting = nil
		return &Wake{Kind: "recheck", Generation: generation, EventIDs: []string{}, Reason: "model_wait"}
	}
	meaningful := false
	for _, event := range c.pending {
		meaningful = meaningful || event.Meaningful || event.Addressed
	}
	wake := &Wake{Kind: "run", Generation: c.generation, EventIDs: append([]string{}, c.pendingOrder...), Reason: "quiet"}
	if !meaningful {
		wake.Kind, wake.Reason = "skip", "no_new_signal"
	} else if now >= c.pending[c.pendingOrder[0]].at+c.config.MaxWaitMS {
		wake.Reason = "max_wait"
	}
	clear(c.pending)
	c.pendingOrder = nil
	c.waiting = nil
	if wake.Kind == "run" {
		c.active = wake
	}
	return wake
}

// Finish releases only this attempt; consumed events are never requeued.
func (c *Coordinator) Finish(wake *Wake, result Result) bool {
	if wake == nil || c.active != wake {
		return false
	}
	c.active = nil
	if result.WaitUntil != nil && wake.Generation == c.generation && c.lastWaitGeneration != wake.Generation && !math.IsNaN(*result.WaitUntil) && !math.IsInf(*result.WaitUntil, 0) {
		now := c.now()
		if *result.WaitUntil > now && c.config.MaxModelWaitMS > 0 {
			c.waiting = &waiting{
				at: math.Min(*result.WaitUntil, now+c.config.MaxModelWaitMS), generation: wake.Generation,
				eventIDs: append([]string{}, wake.EventIDs...),
			}
			c.lastWaitGeneration = wake.Generation
		}
	}
	return true
}

// Cancel clears buffered work, not process-local replay identities.
func (c *Coordinator) Cancel() {
	c.generation++
	clear(c.pending)
	c.pendingOrder = nil
	c.active = nil
	c.waiting = nil
}

func (c *Coordinator) pendingDeadline() (float64, bool) {
	if len(c.pending) == 0 {
		return 0, false
	}
	first := c.pending[c.pendingOrder[0]].at
	last := c.pending[c.pendingOrder[len(c.pendingOrder)-1]].at
	quiet := c.config.QuietMS
	if addressed, _ := c.PendingAddressed(); addressed {
		quiet = math.Min(quiet, c.config.AddressedQuietMS)
	}
	return math.Min(last+quiet, first+c.config.MaxWaitMS), true
}

func (c *Coordinator) removePending(id string) {
	delete(c.pending, id)
	c.pendingOrder = remove(c.pendingOrder, id)
}

func contains(values []string, id string) bool {
	for _, value := range values {
		if value == id {
			return true
		}
	}
	return false
}

func remove(values []string, id string) []string {
	for i, value := range values {
		if value == id {
			copy(values[i:], values[i+1:])
			values[len(values)-1] = ""
			return values[:len(values)-1]
		}
	}
	return values
}
