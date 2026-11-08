// Package learningadmission owns metadata only; it never starts model work or timers.
package learningadmission

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"sort"
	"sync"
	"time"
)

type Options struct{ QuietMS, MaxWaitMS, MinIntervalMS, MaxSourcesPerScene, MaxSourcesTotal, MaxScenes int64 }

func Defaults() Options         { return Options{2000, 8000, 15000, 2048, 8192, 256} }
func SemanticDefaults() Options { return Options{30000, 120000, 60000, 128, 1024, 128} }
func (o Options) Validate() error {
	for _, v := range []int64{o.QuietMS, o.MaxWaitMS, o.MinIntervalMS} {
		if v < 0 || v > 9007199254740991 {
			return errors.New("invalid_learning_admission_option")
		}
	}
	for _, v := range []int64{o.MaxSourcesPerScene, o.MaxSourcesTotal, o.MaxScenes} {
		if v < 1 || v > 9007199254740991 {
			return errors.New("invalid_learning_admission_option")
		}
	}
	if o.MaxWaitMS < o.QuietMS {
		return errors.New("learning_maximum_wait_shorter_than_quiet")
	}
	return nil
}

type Capacity struct {
	ForegroundBusy    bool
	BudgetUnavailable bool
}
type Batch struct {
	SceneID    string
	Token      uint64
	Lane       string
	SourceKeys []string
}
type Status struct {
	State, Reason, Lane    string
	DueAt, FirstAdmittedAt int64
	Pending                int
}
type source struct {
	key  string
	at   int64
	lane string
}
type scene struct {
	pending []source
	active  *Batch
	sources []source
	last    *int64
	retry   *int64
}
type Admission struct {
	mu       sync.Mutex
	collator *topiccollation.Collator
	options  Options
	now      func() int64
	enabled  bool
	sequence uint64
	scenes   map[string]*scene
	order    []string
}

func New(o Options, now func() int64, collator *topiccollation.Collator) (*Admission, error) {
	if collator == nil {
		return nil, errors.New("learning_collation_required")
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Admission{collator: collator, options: o, now: now, enabled: true, scenes: map[string]*scene{}}, nil
}
func (a *Admission) Configure(o Options) error {
	if e := o.Validate(); e != nil {
		return e
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.options = o
	return nil
}
func (a *Admission) SetEnabled(enabled bool) []Batch {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enabled = enabled
	out := []Batch{}
	if !enabled {
		for _, id := range a.order {
			if s := a.scenes[id]; s != nil && s.active != nil {
				out = append(out, clone(*s.active))
			}
		}
		a.scenes = map[string]*scene{}
		a.order = nil
	}
	return out
}
func clone(b Batch) Batch { b.SourceKeys = append([]string(nil), b.SourceKeys...); return b }
func count(s *scene) int  { return len(s.pending) + len(s.sources) }
func (a *Admission) remove(id string) {
	delete(a.scenes, id)
	for i, k := range a.order {
		if k == id {
			a.order = append(a.order[:i], a.order[i+1:]...)
			return
		}
	}
}
func (a *Admission) Admit(id, key, lane string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == "" || key == "" {
		return "", errors.New("learning_admission_requires_scene_and_source")
	}
	if !a.enabled {
		return "disabled", nil
	}
	now := a.now()
	for _, id := range append([]string(nil), a.order...) {
		s := a.scenes[id]
		if count(s) == 0 && (s.last == nil || now >= numberAdd(*s.last, a.options.MinIntervalMS)) {
			a.remove(id)
		}
	}
	s := a.scenes[id]
	if s != nil {
		for _, v := range append(append([]source(nil), s.pending...), s.sources...) {
			if v.key == key {
				return "duplicate", nil
			}
		}
	}
	total := 0
	for _, s := range a.scenes {
		total += count(s)
	}
	if s == nil && int64(len(a.scenes)) >= a.options.MaxScenes || s != nil && int64(count(s)) >= a.options.MaxSourcesPerScene || int64(total) >= a.options.MaxSourcesTotal {
		return "capacity", nil
	}
	if s == nil {
		s = &scene{}
		a.scenes[id] = s
		a.order = append(a.order, id)
	}
	if lane == "" {
		lane = "realtime"
	}
	s.pending = append(s.pending, source{key, now, lane})
	return "accepted", nil
}
func (a *Admission) inspect(id string, c Capacity) Status {
	if !a.enabled {
		return Status{State: "disabled"}
	}
	s := a.scenes[id]
	if s != nil && s.active != nil {
		return Status{State: "active"}
	}
	if s == nil || len(s.pending) == 0 {
		return Status{State: "idle"}
	}
	if c.ForegroundBusy {
		return Status{State: "blocked", Reason: "foreground_busy"}
	}
	if c.BudgetUnavailable {
		return Status{State: "blocked", Reason: "budget_unavailable"}
	}
	first, last := s.pending[0].at, s.pending[0].at
	lane := "background"
	for _, v := range s.pending {
		if v.at < first {
			first = v.at
		}
		if v.at > last {
			last = v.at
		}
		if v.lane == "realtime" {
			lane = "realtime"
		}
	}
	due := min(numberAdd(last, a.options.QuietMS), numberAdd(first, a.options.MaxWaitMS))
	if s.last != nil {
		due = max(due, numberAdd(*s.last, a.options.MinIntervalMS))
	}
	if s.retry != nil {
		due = max(due, *s.retry)
	}
	state := "waiting"
	if a.now() >= due {
		state = "ready"
	}
	return Status{state, "", lane, due, first, len(s.pending)}
}
func (a *Admission) Inspect(id string, c Capacity) Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inspect(id, c)
}
func (a *Admission) ReadyScenes(c Capacity) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []string{}
	statuses := map[string]Status{}
	for _, id := range a.order {
		s := a.inspect(id, c)
		if s.State == "ready" {
			out = append(out, id)
			statuses[id] = s
		}
	}
	var compareErr error
	sort.SliceStable(out, func(i, j int) bool {
		x, y := statuses[out[i]], statuses[out[j]]
		if x.Lane != y.Lane {
			return x.Lane == "realtime"
		}
		if x.FirstAdmittedAt != y.FirstAdmittedAt {
			return x.FirstAdmittedAt < y.FirstAdmittedAt
		}
		order, e := a.collator.Compare(out[i], out[j])
		if e != nil {
			compareErr = e
		}
		return order < 0
	})
	return out, compareErr
}
func (a *Admission) Take(id string, limit int, c Capacity) (*Batch, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if limit < 1 {
		return nil, errors.New("invalid_learning_batch_limit")
	}
	if a.inspect(id, c).State != "ready" {
		return nil, nil
	}
	s := a.scenes[id]
	n := min(limit, len(s.pending))
	s.sources = append([]source(nil), s.pending[:n]...)
	s.pending = append([]source(nil), s.pending[n:]...)
	a.sequence++
	b := Batch{SceneID: id, Token: a.sequence, Lane: "background", SourceKeys: []string{}}
	for _, v := range s.sources {
		b.SourceKeys = append(b.SourceKeys, v.key)
		if v.lane == "realtime" {
			b.Lane = "realtime"
		}
	}
	s.active = &b
	now := a.now()
	s.last = &now
	s.retry = nil
	out := clone(b)
	return &out, nil
}
func (a *Admission) Finish(id string, token uint64, retry *int64) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if retry != nil && (*retry < 0 || *retry > 9007199254740991) {
		return false, errors.New("invalid_learning_retry_delay")
	}
	s := a.scenes[id]
	if s == nil || s.active == nil || s.active.Token != token {
		return false, nil
	}
	if retry != nil {
		s.pending = append(append([]source(nil), s.sources...), s.pending...)
		at := numberAdd(a.now(), *retry)
		s.retry = &at
	}
	s.active = nil
	s.sources = nil
	return true, nil
}
func (a *Admission) Recall(id, key string, requeue bool) *Batch {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.scenes[id]
	if s == nil {
		return nil
	}
	for i, v := range s.pending {
		if v.key == key {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			break
		}
	}
	found := false
	for _, v := range s.sources {
		if v.key == key {
			found = true
		}
	}
	if !found {
		return nil
	}
	b := clone(*s.active)
	if requeue {
		survivors := []source{}
		for _, v := range s.sources {
			if v.key != key {
				survivors = append(survivors, v)
			}
		}
		s.pending = append(survivors, s.pending...)
	}
	s.active = nil
	s.sources = nil
	return &b
}
func (a *Admission) CancelScene(id string) *Batch {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.scenes[id]
	a.remove(id)
	if s == nil || s.active == nil {
		return nil
	}
	b := clone(*s.active)
	return &b
}

// Patch represents source configure's optional fields, preserving explicit zero.
type Patch struct{ QuietMS, MaxWaitMS, MinIntervalMS, MaxSourcesPerScene, MaxSourcesTotal, MaxScenes *int64 }

func (a *Admission) ConfigurePatch(p Patch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.options
	for _, field := range []struct {
		from *int64
		to   *int64
	}{{p.QuietMS, &next.QuietMS}, {p.MaxWaitMS, &next.MaxWaitMS}, {p.MinIntervalMS, &next.MinIntervalMS}, {p.MaxSourcesPerScene, &next.MaxSourcesPerScene}, {p.MaxSourcesTotal, &next.MaxSourcesTotal}, {p.MaxScenes, &next.MaxScenes}} {
		if field.from != nil {
			*field.to = *field.from
		}
	}
	if err := next.Validate(); err != nil {
		return err
	}
	a.options = next
	return nil
}

// Source times are JavaScript Numbers, including rounding above the safe sum.
func numberAdd(a, b int64) int64 { return int64(float64(a) + float64(b)) }
