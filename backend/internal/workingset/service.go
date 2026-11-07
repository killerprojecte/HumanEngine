package workingset

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Eligibility/readiness observers receive the exact active scope; write
// observers receive its actual child session. Resolve only exact authorized
// references, never upgrade a reference or reenter Authority.
type Ports struct {
	ObserveReady   func(*Scope) error
	Enabled        func() bool
	ResolveCurrent func(*Scope, Context, []Reference) ([]Snapshot, error)
	BeforeWrite    func(*recovery.WriteSession) error
	// ObservePinWrite observes the validated intent before the mandatory durable
	// append. It cannot replace journaling or enrollment; returning an error aborts.
	ObservePinWrite func(*recovery.WriteSession, PinPolicy) error
	Now             func() float64
	Token           func() (string, error)
	Policy          *Policy
}
type Service struct {
	main         *sql.DB
	authority    *recovery.Authority
	session      *recovery.WriteSession
	readScope    *Scope
	bindingError error
	ports        Ports
	policy       *policyState
}

func New(ctx context.Context, main *sql.DB, a *recovery.Authority, ports Ports) (*Service, error) {
	if main == nil || a == nil || ports.Enabled == nil || ports.ResolveCurrent == nil {
		return nil, errors.New("working_set_dependencies_required")
	}
	p := DefaultPolicy()
	if ports.Policy != nil {
		p = *ports.Policy
	}
	if ports.Now == nil {
		ports.Now = func() float64 { return float64(time.Now().UnixMilli()) }
	}
	if ports.Token == nil {
		ports.Token = randomToken
	}
	s := &Service{main: main, authority: a, ports: ports, policy: &policyState{value: p}}
	e := s.Read(ctx, func(b *Service) error { _, e := b.Storage().AssertSupported(); return e })
	if e != nil {
		return nil, e
	}
	if e = p.Validate(); e != nil {
		return nil, e
	}
	return s, nil
}
func randomToken() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func (s *Service) WithSession(session *recovery.WriteSession) *Service {
	b := *s
	b.session = session
	b.readScope = nil
	if session == nil {
		b.bindingError = errors.New("working_set_transaction_required")
	} else {
		b.bindingError = session.CheckBinding(s.authority, s.main)
	}
	return &b
}
func (s *Service) Session() *recovery.WriteSession { return s.session }
func (s *Service) Storage() *Storage               { return &Storage{session: s.session, scope: s.scope()} }
func (s *Service) check() error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session == nil {
		return errors.New("working_set_transaction_required")
	}
	return s.session.CheckBinding(s.authority, s.main)
}
func (s *Service) ready() error {
	if e := s.scope().CheckScope(); e != nil {
		return e
	}
	if s.ports.ObserveReady != nil {
		if e := s.ports.ObserveReady(s.scope()); e != nil {
			return e
		}
	}
	if s.session != nil {
		return s.session.AssertReady()
	}
	return nil
}
func (s *Service) Transaction(ctx context.Context, fn func(*Service) error) error {
	if s.readScope != nil {
		return errors.New("working_set_read_only_scope")
	}
	if s.bindingError != nil {
		return s.bindingError
	}
	if fn == nil {
		return errors.New("working_set_callback_required")
	}
	run := func(session *recovery.WriteSession) error { return fn(s.WithSession(session)) }
	if s.session != nil {
		return s.session.WithSavepoint(run)
	}
	return s.authority.WithWriteSession(ctx, s.main, run)
}
func (s *Service) ConfigurePolicy(cold, review *float64) error {
	return s.policy.configure(cold, review)
}

func (s *Service) now() (float64, error) {
	v := s.ports.Now()
	if !safe(v) {
		return 0, errors.New("working_set_clock_invalid")
	}
	return v, nil
}
func (s *Service) enabled(ctx Context) (bool, error) {
	if e := s.ready(); e != nil {
		return false, e
	}
	return !ctx.Simulation && s.ports.Enabled(), nil
}
func (s *Service) write() error {
	if s.ports.BeforeWrite != nil {
		if e := s.ports.BeforeWrite(s.session); e != nil {
			return e
		}
	}
	if e := s.session.RequireReader(4); e != nil {
		return e
	}
	return s.Storage().Ensure()
}
func (s *Service) writePin(p PinPolicy) error {
	if e := validate(p); e != nil {
		return e
	}
	if e := s.write(); e != nil {
		return e
	}
	if s.ports.ObservePinWrite != nil {
		copy, e := clone(p)
		if e != nil {
			return e
		}
		if e = s.ports.ObservePinWrite(s.session, copy); e != nil {
			return e
		}
	}
	if e := s.session.RequireReader(4); e != nil {
		return e
	}
	if _, e := s.session.AppendIntent(recovery.WorkingSetPin{Policy: p}); e != nil {
		return e
	}
	return s.Storage().PutPin(p)
}
func clone[T any](value T) (T, error) {
	raw, e := Encode(value)
	if e != nil {
		var zero T
		return zero, e
	}
	return Decode[T](raw)
}
func (s *Service) bounded(input []Selection) ([]Selection, bool, error) {
	n := len(input)
	if n > int(s.policy.load().MaxBatch) {
		n = int(s.policy.load().MaxBatch)
	}
	out := []Selection{}
	seen := map[string]int{}
	for _, raw := range input[:n] {
		value, e := clone(raw)
		if e != nil {
			return nil, false, e
		}
		key := ReferenceKey(value.Ref)
		if i, ok := seen[key]; ok {
			if out[i].DependencyKey != value.DependencyKey {
				return nil, false, errors.New("working_set_selection_conflict")
			}
			out[i] = value
		} else {
			seen[key] = len(out)
			out = append(out, value)
		}
	}
	return out, len(input) > n, nil
}
func (s *Service) resolve(ctx Context, input []Selection) (map[string]Snapshot, []Snapshot, error) {
	result := map[string]Snapshot{}
	ordered := []Snapshot{}
	if len(input) == 0 {
		return result, ordered, nil
	}
	wanted := map[string]Selection{}
	refs := []Reference{}
	for _, v := range input {
		wanted[ReferenceKey(v.Ref)] = v
		copy, e := clone(v.Ref)
		if e != nil {
			return nil, nil, e
		}
		refs = append(refs, copy)
	}
	snapshots, e := s.ports.ResolveCurrent(s.scope(), ctx, refs)
	if e != nil {
		return nil, nil, e
	}
	if len(snapshots) > int(s.policy.load().MaxBatch) {
		return nil, nil, errors.New("working_set_authority_invalid")
	}
	for _, raw := range snapshots {
		value, e := clone(raw)
		if e != nil {
			return nil, nil, e
		}
		selected, ok := wanted[ReferenceKey(value.Ref)]
		if !ok || value.SceneID != ctx.SceneID || !SameSelection(selected, value.Selection()) {
			continue
		}
		key := SelectionKey(value.Selection())
		if _, ok = result[key]; ok {
			return nil, nil, errors.New("working_set_authority_duplicate")
		}
		result[key] = value
		ordered = append(ordered, value)
	}
	return result, ordered, nil
}
func (s *Service) previous(snapshot Snapshot) (*Item, error) {
	v, e := s.Storage().Item(snapshot.SceneID, snapshot.Ref)
	if e != nil {
		return nil, e
	}
	if v != nil && SameSelection(v.Snapshot.Selection(), snapshot.Selection()) {
		return v, nil
	}
	return nil, nil
}
func makeItem(snapshot Snapshot, previous *Item, now float64) Item {
	v := Item{SchemaVersion: 1, Snapshot: snapshot, MetadataRevision: 1, ProjectedAt: now}
	if previous != nil {
		v.MetadataRevision = previous.MetadataRevision
		v.ContextSelectedCount = previous.ContextSelectedCount
		v.LastSelectedAt = previous.LastSelectedAt
	}
	return v
}

type ViewResult struct {
	Enabled     bool       `json:"enabled"`
	Items       []ViewItem `json:"items"`
	Coverage    string     `json:"coverage"`
	OmittedCold int        `json:"omittedCold"`
	Unavailable int        `json:"unavailable"`
}

func (s *Service) View(ctx context.Context, c Context, input []Selection, includeCold bool) (out ViewResult, err error) {
	err = s.Read(ctx, func(b *Service) error { var e error; out, e = b.view(c, input, includeCold); return e })
	return
}
func (s *Service) view(ctx Context, input []Selection, includeCold bool) (ViewResult, error) {
	out := ViewResult{Items: []ViewItem{}, Coverage: "disabled"}
	yes, e := s.enabled(ctx)
	if e != nil || !yes {
		return out, e
	}
	batch, limited, e := s.bounded(input)
	if e != nil {
		return out, e
	}
	current, _, e := s.resolve(ctx, batch)
	if e != nil {
		return out, e
	}
	now, e := s.now()
	if e != nil {
		return out, e
	}
	out.Enabled = true
	out.Coverage = "complete"
	out.Unavailable = len(batch) - len(current)
	for _, selected := range batch {
		snapshot, ok := current[SelectionKey(selected)]
		if !ok {
			continue
		}
		item, e := s.previous(snapshot)
		if e != nil {
			return out, e
		}
		p, e := s.Storage().Pin(ctx.SceneID, entity(snapshot.Ref))
		if e != nil {
			return out, e
		}
		v := ViewItem{Ref: snapshot.Ref, DependencyKey: snapshot.DependencyKey, PinStatus: "none", PolicyCoverage: "complete", Protection: snapshot.Protection}
		unresolved := false
		if p != nil {
			exact := SameSelection(p.Selection, snapshot.Selection())
			v.PinIntent = p.Pinned
			v.PinRevision = p.PolicyRevision
			if p.Terminal {
				v.PinStatus = "terminal"
			} else if exact {
				v.PinStatus = "current"
			} else {
				v.PinStatus = "stale"
			}
			unresolved = p.Pinned && !p.Terminal && !exact
			v.Pinned = p.Pinned && !p.Terminal && exact
		}
		if unresolved {
			v.PolicyCoverage = "unknown"
		}
		cl := Classify(snapshot, item, now, s.policy.load(), v.Pinned, unresolved)
		v.State = cl.State
		v.ReviewDue = cl.ReviewDue
		if !includeCold && v.State == "cold" {
			out.OmittedCold++
			continue
		}
		if item != nil {
			v.MetadataRevision = item.MetadataRevision
			v.ContextSelectedCount = item.ContextSelectedCount
			v.LastSelectedAt = item.LastSelectedAt
		}
		out.Items = append(out.Items, v)
		if unresolved || (v.Protection != nil && *v.Protection == "unresolved_commitment_coverage") {
			limited = true
		}
	}
	if limited || out.Unavailable > 0 || out.OmittedCold > 0 {
		out.Coverage = "limited"
	}
	return out, s.ready()
}

type SelectedInput struct {
	TurnID     string      `json:"turnId"`
	SelectedAt float64     `json:"selectedAt"`
	Selections []Selection `json:"selections"`
}
type SelectedResult struct {
	Recorded    int    `json:"recorded"`
	Duplicates  int    `json:"duplicates"`
	Unavailable int    `json:"unavailable"`
	Coverage    string `json:"coverage"`
}

func (s *Service) RecordSelected(ctx context.Context, c Context, input SelectedInput) (out SelectedResult, err error) {
	err = s.Transaction(ctx, func(b *Service) error { var e error; out, e = b.recordSelected(c, input); return e })
	return
}
func (s *Service) recordSelected(ctx Context, input SelectedInput) (SelectedResult, error) {
	out := SelectedResult{Coverage: "disabled"}
	yes, e := s.enabled(ctx)
	if e != nil || !yes {
		return out, e
	}
	now, e := s.now()
	if e != nil {
		return out, e
	}
	if !explicitID(input.TurnID) || !safe(input.SelectedAt) || input.SelectedAt > now || input.SelectedAt < now-s.policy.load().SelectionMaxAgeMs {
		return out, errors.New("working_set_selection_invalid")
	}
	batch, limited, e := s.bounded(input.Selections)
	if e != nil {
		return out, e
	}
	for _, v := range batch {
		if v.Ref.Kind != "assertion" {
			return out, errors.New("working_set_plan_metrics_unsupported")
		}
	}
	current, _, e := s.resolve(ctx, batch)
	if e != nil {
		return out, e
	}
	store := s.Storage()
	for _, selected := range batch {
		snapshot, ok := current[SelectionKey(selected)]
		if !ok {
			continue
		}
		raw, _ := jsjson.Stringify([]any{ctx.SceneID, input.TurnID, ReferenceKey(selected.Ref)})
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
		receipt, e := store.Receipt(key)
		if e != nil {
			return out, e
		}
		if receipt != nil {
			out.Duplicates++
			continue
		}
		if e = s.write(); e != nil {
			return out, e
		}
		if e = store.PruneReceipts(now - s.policy.load().ReceiptRetentionMs); e != nil {
			return out, e
		}
		count, e := store.ReceiptCount()
		if e != nil {
			return out, e
		}
		if float64(count) >= s.policy.load().MaxReceipts {
			limited = true
			continue
		}
		previous, e := s.previous(snapshot)
		if e != nil {
			return out, e
		}
		item := makeItem(snapshot, previous, now)
		if e = store.PutReceipt(key, Receipt{1, ctx.SceneID, input.TurnID, selected, input.SelectedAt}); e != nil {
			return out, e
		}
		if previous != nil {
			item.MetadataRevision = previous.MetadataRevision + 1
		}
		item.ContextSelectedCount = math.Min(MaxSafe, item.ContextSelectedCount+1)
		last := 0.0
		if item.LastSelectedAt != nil {
			last = *item.LastSelectedAt
		}
		last = math.Max(last, input.SelectedAt)
		item.LastSelectedAt = &last
		if e = store.PutItem(item); e != nil {
			return out, e
		}
		out.Recorded++
	}
	out.Unavailable = len(batch) - len(current)
	out.Coverage = "complete"
	if limited || out.Unavailable > 0 {
		out.Coverage = "limited"
	}
	return out, s.ready()
}

type PinList struct {
	Items    []PinPolicy `json:"items"`
	Coverage string      `json:"coverage"`
}

func (s *Service) ListPinPolicies(ctx context.Context, c Context, limit float64) (out PinList, err error) {
	out = PinList{[]PinPolicy{}, "complete"}
	err = s.Read(ctx, func(b *Service) error {
		if e := b.ready(); e != nil {
			return e
		}
		if !safe(limit) || limit < 1 || limit > 200 {
			return errors.New("working_set_limit_invalid")
		}
		if c.Simulation {
			return nil
		}
		rows, e := b.Storage().ListPins(c.SceneID, int(limit)+1)
		if e != nil {
			return e
		}
		if e = b.ready(); e != nil {
			return e
		}
		if len(rows) > int(limit) {
			out.Coverage = "limited"
			rows = rows[:int(limit)]
		}
		out.Items = rows
		return nil
	})
	return
}
func (s *Service) SetPinned(ctx context.Context, c Context, selected Selection, expected float64, pinned bool) (out PinPolicy, err error) {
	err = s.Transaction(ctx, func(b *Service) error {
		if e := b.ready(); e != nil {
			return e
		}
		if c.Simulation {
			return errors.New("working_set_disabled")
		}
		if !safe(expected) {
			return errors.New("working_set_pin_invalid")
		}
		value, e := clone(selected)
		if e != nil {
			return e
		}
		prior, e := b.Storage().Pin(c.SceneID, entity(value.Ref))
		if e != nil {
			return e
		}
		revision := 0.0
		if prior != nil {
			revision = prior.PolicyRevision
		}
		if revision != expected {
			return errors.New("working_set_policy_conflict")
		}
		if prior != nil && prior.Terminal {
			return errors.New("working_set_policy_terminal")
		}
		if pinned {
			if !b.ports.Enabled() {
				return errors.New("working_set_disabled")
			}
			current, _, e := b.resolve(c, []Selection{value})
			if e != nil {
				return e
			}
			if _, ok := current[SelectionKey(value)]; !ok {
				return errors.New("working_set_source_stale")
			}
		} else if prior == nil {
			return errors.New("working_set_policy_not_found")
		}
		anchor := value
		if !pinned {
			anchor = prior.Selection
		}
		if prior != nil && prior.Pinned == pinned && SameSelection(prior.Selection, anchor) {
			out = *prior
			return nil
		}
		out = PinPolicy{SchemaVersion: 1, SceneID: c.SceneID, Selection: anchor, PolicyRevision: revision + 1, Pinned: pinned, Terminal: false}
		if e = b.writePin(out); e != nil {
			return e
		}
		return b.ready()
	})
	return
}
func (s *Service) TerminalInvalidate(ctx context.Context, scene string, refs []Entity) error {
	return s.Transaction(ctx, func(b *Service) error {
		if e := b.ready(); e != nil {
			return e
		}
		store := b.Storage()
		yes, e := store.Exists()
		if e != nil || !yes {
			return e
		}
		for _, ref := range refs {
			p, e := store.Pin(scene, ref)
			if e != nil {
				return e
			}
			if p != nil && !p.Terminal {
				p.PolicyRevision++
				p.Pinned = false
				p.Terminal = true
				if e = b.writePin(*p); e != nil {
					return e
				}
			}
		}
		return store.Invalidate(scene, refs)
	})
}

type EnqueueResult struct {
	Queued   int    `json:"queued"`
	Coverage string `json:"coverage"`
}

func (s *Service) Enqueue(ctx context.Context, c Context, input []Selection) (out EnqueueResult, err error) {
	out.Coverage = "disabled"
	err = s.Transaction(ctx, func(b *Service) error {
		yes, e := b.enabled(c)
		if e != nil || !yes {
			return e
		}
		batch, limited, e := b.bounded(input)
		if e != nil {
			return e
		}
		_, current, e := b.resolve(c, batch)
		if e != nil {
			return e
		}
		store := b.Storage()
		for _, snapshot := range current {
			previous, e := b.previous(snapshot)
			if e != nil {
				return e
			}
			if previous != nil {
				continue
			}
			job, e := store.Job(c.SceneID, snapshot.Ref)
			if e != nil {
				return e
			}
			if job != nil && SameSelection(job.Snapshot.Selection(), snapshot.Selection()) {
				continue
			}
			if e = b.write(); e != nil {
				return e
			}
			token, e := b.ports.Token()
			if e != nil {
				return e
			}
			now, e := b.now()
			if e != nil {
				return e
			}
			if e = store.PutJob(Job{1, snapshot, token, now, nil}); e != nil {
				return e
			}
			out.Queued++
		}
		out.Coverage = "complete"
		if limited {
			out.Coverage = "limited"
		}
		return nil
	})
	return
}
func (s *Service) Claim(ctx context.Context, c Context, limit float64) (out []Lease, err error) {
	out = []Lease{}
	err = s.Transaction(ctx, func(b *Service) error {
		yes, e := b.enabled(c)
		if e != nil || !yes {
			return e
		}
		if !safe(limit) || limit < 1 {
			return errors.New("working_set_limit_invalid")
		}
		now, e := b.now()
		if e != nil {
			return e
		}
		jobs, e := b.Storage().Jobs(c.SceneID, now, int(math.Min(limit, b.policy.load().MaxBatch)))
		if e != nil || len(jobs) == 0 {
			return e
		}
		if e = b.write(); e != nil {
			return e
		}
		for _, job := range jobs {
			token, e := b.ports.Token()
			if e != nil {
				return e
			}
			job.Lease = &JobLease{token, now + b.policy.load().LeaseMs}
			if e = b.Storage().PutJob(job); e != nil {
				return e
			}
			out = append(out, Lease{c.SceneID, job.Snapshot.Ref, job.Snapshot.DependencyKey, job.Generation, token, job.Lease.ExpiresAt})
		}
		return nil
	})
	return
}

type CompleteResult struct {
	Completed int    `json:"completed"`
	Stale     int    `json:"stale"`
	Coverage  string `json:"coverage"`
}

func (s *Service) Complete(ctx context.Context, c Context, leases []Lease) (out CompleteResult, err error) {
	out = CompleteResult{0, len(leases), "disabled"}
	err = s.Transaction(ctx, func(b *Service) error {
		yes, e := b.enabled(c)
		if e != nil || !yes {
			return e
		}
		bounded := leases
		if len(bounded) > int(b.policy.load().MaxBatch) {
			bounded = bounded[:int(b.policy.load().MaxBatch)]
		}
		now, e := b.now()
		if e != nil {
			return e
		}
		valid := []Lease{}
		selections := []Selection{}
		for _, l := range bounded {
			if l.SceneID != c.SceneID || l.ExpiresAt <= now {
				continue
			}
			job, e := b.Storage().Job(c.SceneID, l.Ref)
			if e != nil {
				return e
			}
			if job != nil && job.Generation == l.Generation && job.Lease != nil && job.Lease.Token == l.Token && job.Lease.ExpiresAt == l.ExpiresAt && SameSelection(job.Snapshot.Selection(), l.Selection()) {
				valid = append(valid, l)
				selections = append(selections, l.Selection())
			}
		}
		current, _, e := b.resolve(c, selections)
		if e != nil {
			return e
		}
		for _, l := range valid {
			job, e := b.Storage().Job(c.SceneID, l.Ref)
			if e != nil {
				return e
			}
			if job == nil || job.Generation != l.Generation || job.Lease == nil || job.Lease.Token != l.Token {
				continue
			}
			snapshot, ok := current[SelectionKey(l.Selection())]
			if e = b.write(); e != nil {
				return e
			}
			if ok {
				previous, e := b.previous(snapshot)
				if e != nil {
					return e
				}
				if e = b.Storage().PutItem(makeItem(snapshot, previous, now)); e != nil {
					return e
				}
				out.Completed++
			}
			if e = b.Storage().RemoveJob(c.SceneID, l.Ref); e != nil {
				return e
			}
		}
		if e = b.ready(); e != nil {
			return e
		}
		out.Stale = len(bounded) - out.Completed
		out.Coverage = "complete"
		if len(leases) > len(bounded) {
			out.Coverage = "limited"
		}
		return nil
	})
	return
}

type DrainResult struct {
	Completed int    `json:"completed"`
	Stale     int    `json:"stale"`
	Coverage  string `json:"coverage"`
	Pending   int    `json:"pending"`
}

func (s *Service) Drain(ctx context.Context, c Context, limit float64) (out DrainResult, err error) {
	leases, e := s.Claim(ctx, c, limit)
	if e != nil {
		return out, e
	}
	result, e := s.Complete(ctx, c, leases)
	if e != nil {
		return out, e
	}
	out.Completed = result.Completed
	out.Stale = result.Stale
	out.Coverage = result.Coverage
	err = s.Transaction(ctx, func(b *Service) error { var e error; out.Pending, e = b.Storage().Pending(c.SceneID); return e })
	return
}
func (s *Service) CancelMaintenance(ctx context.Context, scene *string) error {
	return s.Transaction(ctx, func(b *Service) error {
		if e := b.ready(); e != nil {
			return e
		}
		yes, e := b.Storage().Exists()
		if e != nil || !yes {
			return e
		}
		if e = b.write(); e != nil {
			return e
		}
		if scene != nil && *scene != "" {
			_, e = b.Storage().exec("DELETE FROM memory_working_set_jobs WHERE scene_id=?", *scene)
		} else {
			_, e = b.Storage().exec("DELETE FROM memory_working_set_jobs")
		}
		return e
	})
}
func (s *Service) Invalidate(ctx context.Context, scene string, refs []Entity) error {
	return s.Transaction(ctx, func(b *Service) error {
		if e := b.ready(); e != nil {
			return e
		}
		yes, e := b.Storage().Exists()
		if e != nil || !yes || len(refs) == 0 {
			return e
		}
		if e = b.write(); e != nil {
			return e
		}
		return b.Storage().Invalidate(scene, refs)
	})
}
