package memoryservice

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type CommitmentHistoryExposure struct{ owner *commitmentRecordedExposure }
type CommitmentTimelineExposure struct{ owner *commitmentRecordedExposure }
type commitmentRecordedExposure struct {
	current  *CommitmentExposure
	timeline bool
}
type commitmentRecordedEntry struct {
	exposure *StateExposure
	values   []*Object
}
type commitmentRecordedTurn struct {
	operation sync.Mutex
	mu        sync.Mutex
	entries   []*commitmentRecordedEntry
}
type commitmentRecordedPort struct {
	owner          *commitmentRecordedExposure
	anchor         commitments.Exposure
	options        *Object
	initial, first *Object
}

func newCommitmentRecorded(memory *ExposurePort, current *CommitmentExposure, timeline bool) (*commitmentRecordedExposure, error) {
	if memory == nil || current == nil || current.port.memory != memory {
		return nil, errors.New("commitment_recorded_exposure_owners_required")
	}
	return &commitmentRecordedExposure{current, timeline}, nil
}
func NewCommitmentHistoryExposure(memory *ExposurePort, current *CommitmentExposure) (*CommitmentHistoryExposure, error) {
	e, err := newCommitmentRecorded(memory, current, false)
	if err != nil {
		return nil, err
	}
	return &CommitmentHistoryExposure{e}, nil
}
func NewCommitmentTimelineExposure(memory *ExposurePort, current *CommitmentExposure) (*CommitmentTimelineExposure, error) {
	e, err := newCommitmentRecorded(memory, current, true)
	if err != nil {
		return nil, err
	}
	return &CommitmentTimelineExposure{e}, nil
}
func (e *commitmentRecordedExposure) turn(c *botruntime.TurnContext) *commitmentRecordedTurn {
	return c.OwnedExtensionState(e, func() any { return new(commitmentRecordedTurn) }).(*commitmentRecordedTurn)
}
func exposureRef(r commitments.Exposure) *Object {
	return epObj("id", r.ID, "version", r.Version, "deletionEpoch", r.DeletionEpoch, "mutationVersion", r.MutationVersion)
}
func sameCommitmentRef(v *Object, r commitments.Reference) bool {
	return text(v, "id") == r.ID && commitmentNumber(v, "version") == r.Version && commitmentNumber(v, "deletionEpoch") == r.DeletionEpoch
}
func (e *commitmentRecordedExposure) withRead(ctx context.Context, c Context, fn func(*commitments.ReadService, commitments.Context) error) (allowed bool, err error) {
	err = e.current.port.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		var err error
		allowed, err = e.current.port.withRead(reads, c, true, fn)
		return err
	})
	return
}
func (e *commitmentRecordedExposure) query(ctx context.Context, c *botruntime.TurnContext, ref commitments.Reference, options *Object) ([]*Object, error) {
	empty := []*Object{}
	if !stateReal(c) {
		return empty, nil
	}
	state := e.turn(c)
	state.operation.Lock()
	defer state.operation.Unlock()
	allowed, err := e.withRead(ctx, exposureContext(c), func(*commitments.ReadService, commitments.Context) error { return nil })
	if err != nil || !allowed {
		return empty, err
	}
	refs, err := e.current.ReadExposedRefs(ctx, c)
	if err != nil {
		return nil, err
	}
	matches := []commitments.Exposure{}
	for _, r := range refs {
		if r.Reference == ref {
			matches = append(matches, r)
		}
	}
	if len(matches) != 1 {
		return empty, nil
	}
	if options == nil {
		options = new(Object)
	}
	p := &commitmentRecordedPort{owner: e, anchor: matches[0], options: exposureClone(options).(*Object)}
	allowed, err = e.withRead(ctx, exposureContext(c), func(reader *commitments.ReadService, scope commitments.Context) error {
		current, err := reader.Current(scope, p.anchor.Reference)
		if err != nil {
			return err
		}
		if current == nil || commitmentNumber(current, "mutationVersion") != p.anchor.MutationVersion || !sameCommitmentRef(object(value(current, "ref")), ref) || !e.timeline && text(current, "sceneId") != scope.SceneID {
			return nil
		}
		p.initial = exposureClone(current).(*Object)
		p.first, err = p.snapshot(reader, scope)
		return err
	})
	if err != nil || !allowed || p.first == nil {
		return empty, err
	}
	exposure, err := NewStateExposure(p, nil)
	if err != nil {
		return nil, err
	}
	values, err := exposure.Query(ctx, c, nil)
	if err != nil {
		return nil, err
	}
	if len(values) > 0 {
		state.mu.Lock()
		state.entries = append(state.entries, &commitmentRecordedEntry{exposure, values})
		state.mu.Unlock()
	}
	return values, nil
}
func (p *commitmentRecordedPort) snapshot(reader *commitments.ReadService, c commitments.Context) (*Object, error) {
	if p.owner.timeline {
		q := commitments.TimelineQuery{}
		if v, ok := p.options.Get("limit"); ok && v != nil && !jsjson.IsUndefined(v) {
			n, ok := v.(float64)
			if !ok {
				return nil, errors.New("commitment_temporal_query_invalid")
			}
			q.Limit = &n
		}
		if v, ok := p.options.Get("asOfRecorded"); ok && !jsjson.IsUndefined(v) {
			s, ok := v.(string)
			if !ok {
				return nil, errors.New("commitment_temporal_query_invalid")
			}
			q.AsOfRecorded = &s
		}
		result, err := reader.TemporalHistory(c, p.anchor, []commitments.Exposure{p.anchor}, q)
		if err != nil || result == nil {
			return nil, err
		}
		if text(result, "interpretation") != "previously_recorded_declaration_state_not_current_truth" || text(result, "coverage") != "limited" || commitmentNumber(object(value(result, "window")), "throughMutationVersion") != p.anchor.MutationVersion {
			return nil, errors.New("commitment_timeline_qualification_invalid")
		}
		out := exposureClone(result).(*Object)
		_ = out.Set("stateKind", "commitment_timeline")
		_ = out.Set("anchor", exposureRef(p.anchor))
		if v := value(p.options, "asOfRecorded"); truthy(v) && !jsjson.IsUndefined(v) {
			_ = out.Set("asOfRecorded", v)
		}
		return out, nil
	}
	history, err := reader.History(c, p.anchor.Reference, []commitments.Exposure{p.anchor})
	if err != nil || history == nil {
		return nil, err
	}
	if text(history, "coverage") != "limited" || text(history, "interpretation") != "previously_recorded_not_current_truth" {
		return nil, errors.New("commitment_history_qualification_invalid")
	}
	revisions, _ := value(history, "revisions").([]any)
	ordered := []any{}
	for _, raw := range revisions {
		v := object(raw)
		r := object(value(v, "ref"))
		if text(r, "id") != p.anchor.ID || commitmentNumber(r, "version") > p.anchor.Version {
			continue
		}
		if text(v, "sceneId") != c.SceneID || commitmentNumber(r, "deletionEpoch") != p.anchor.DeletionEpoch {
			return nil, errors.New("commitment_history_scope_invalid")
		}
		ordered = append(ordered, v)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return commitmentNumber(object(value(object(ordered[i]), "ref")), "version") < commitmentNumber(object(value(object(ordered[j]), "ref")), "version")
	})
	omitted := max(0, len(ordered)-20)
	ordered = ordered[omitted:]
	return epObj("stateKind", "commitment_history", "anchor", exposureRef(p.anchor), "revisions", ordered, "coverage", "limited", "interpretation", "previously_recorded_not_current_truth", "sourceBodyCoverage", "unavailable", "historyWindow", epObj("maxRevisions", float64(20), "omittedRevisions", float64(omitted), "throughVersion", p.anchor.Version)), nil
}
func (p *commitmentRecordedPort) Query(context.Context, Context, any) ([]*Object, error) {
	return []*Object{p.first}, nil
}
func (p *commitmentRecordedPort) validate(reader *commitments.ReadService, c commitments.Context, values []*Object) (bool, error) {
	current, err := reader.Current(c, p.anchor.Reference)
	if err != nil || !deepEqual(current, p.initial) {
		return false, err
	}
	if len(values) != 1 {
		return false, nil
	}
	snapshot, err := p.snapshot(reader, c)
	return err == nil && deepEqual(snapshot, values[0]), err
}
func (p *commitmentRecordedPort) Valid(ctx context.Context, c Context, values []*Object) (valid bool, err error) {
	allowed, err := p.owner.withRead(ctx, c, func(r *commitments.ReadService, c commitments.Context) error {
		var err error
		valid, err = p.validate(r, c, values)
		return err
	})
	return allowed && valid, err
}
func (p *commitmentRecordedPort) ValidScoped(reads botruntime.ReadAuthority, c Context, values []*Object) (bool, error) {
	valid := false
	allowed, err := p.owner.current.port.withRead(reads, c, true, func(r *commitments.ReadService, c commitments.Context) error {
		var err error
		valid, err = p.validate(r, c, values)
		return err
	})
	if errors.Is(err, sql.ErrTxDone) || err != nil && err.Error() == "runtime_read_scope_foreign" {
		return false, nil
	}
	return allowed && valid, err
}
func (p *commitmentRecordedPort) SourceIDs(values []*Object) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	appendView := func(v *Object) {
		for _, id := range commitmentViewSources(v) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	for _, v := range values {
		if !p.owner.timeline {
			revs, _ := value(v, "revisions").([]any)
			for _, r := range revs {
				appendView(object(r))
			}
		} else {
			rows, _ := value(v, "recordedStates").([]any)
			for _, r := range rows {
				states, _ := value(object(r), "states").([]any)
				for _, s := range states {
					appendView(object(s))
				}
			}
		}
	}
	return out, nil
}
func (e *commitmentRecordedExposure) entries(c *botruntime.TurnContext) []*commitmentRecordedEntry {
	s := e.turn(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*commitmentRecordedEntry{}, s.entries...)
}
func (e *commitmentRecordedExposure) deliver(ctx context.Context, c *botruntime.TurnContext, values []any) ([]*Object, error) {
	if len(values) == 0 {
		return []*Object{}, nil
	}
	first, _ := values[0].(*Object)
	for _, entry := range e.entries(c) {
		if len(entry.values) > 0 && entry.values[0] == first {
			return entry.exposure.Deliver(ctx, c, values)
		}
	}
	return []*Object{}, nil
}
func (e *commitmentRecordedExposure) readDelivered(ctx context.Context, c *botruntime.TurnContext) ([]*Object, error) {
	out := []*Object{}
	for _, entry := range e.entries(c) {
		v, err := entry.exposure.ReadDelivered(ctx, c)
		if err != nil {
			return nil, err
		}
		out = append(out, v...)
	}
	return out, nil
}
func (e *commitmentRecordedExposure) capture(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	guards := []*botruntime.ToolResultGuard{}
	for _, entry := range e.entries(c) {
		if g := entry.exposure.CaptureGuard(c); g != nil {
			guards = append(guards, g)
		}
	}
	if len(guards) == 0 {
		return nil
	}
	return &botruntime.ToolResultGuard{Check: func(reads botruntime.ReadAuthority) (bool, error) {
		for _, g := range guards {
			ok, err := g.Check(reads)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}}
}
func (e *CommitmentHistoryExposure) Query(ctx context.Context, c *botruntime.TurnContext, ref commitments.Reference) ([]*Object, error) {
	return e.owner.query(ctx, c, ref, nil)
}
func (e *CommitmentTimelineExposure) Query(ctx context.Context, c *botruntime.TurnContext, ref commitments.Reference, input *Object) ([]*Object, error) {
	return e.owner.query(ctx, c, ref, input)
}
func (e *CommitmentHistoryExposure) Deliver(ctx context.Context, c *botruntime.TurnContext, v []any) ([]*Object, error) {
	return e.owner.deliver(ctx, c, v)
}
func (e *CommitmentTimelineExposure) Deliver(ctx context.Context, c *botruntime.TurnContext, v []any) ([]*Object, error) {
	return e.owner.deliver(ctx, c, v)
}
func (e *CommitmentHistoryExposure) ReadDelivered(ctx context.Context, c *botruntime.TurnContext) ([]*Object, error) {
	return e.owner.readDelivered(ctx, c)
}
func (e *CommitmentHistoryExposure) CaptureGuard(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	return e.owner.capture(c)
}
func (e *CommitmentTimelineExposure) CaptureGuard(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	return e.owner.capture(c)
}
func (e *commitmentRecordedExposure) compose(base botruntime.Extension) *botruntime.Extension {
	ext := base
	ext.CaptureToolResultGuards = func(c *botruntime.TurnContext) []*botruntime.ToolResultGuard {
		out := []*botruntime.ToolResultGuard{}
		if base.CaptureToolResultGuards != nil {
			out = append(out, base.CaptureToolResultGuards(c)...)
		}
		if g := e.capture(c); g != nil {
			out = append(out, g)
		}
		return out
	}
	name := "memory.commitmentHistory"
	if e.timeline {
		name = "memory.commitmentTimeline"
	}
	ext.Delivered = func(tool string, v any, c *botruntime.TurnContext, input any) (any, error) {
		if tool == name {
			var selected []any
			switch a := v.(type) {
			case []any:
				selected = a
			case []*Object:
				selected = make([]any, len(a))
				for i, b := range a {
					selected[i] = b
				}
			}
			if selected != nil {
				out, err := e.deliver(context.Background(), c, selected)
				if err != nil {
					return nil, err
				}
				selected = make([]any, len(out))
				for i, b := range out {
					selected[i] = b
				}
				v = selected
			}
		}
		if base.Delivered != nil {
			return base.Delivered(tool, v, c, input)
		}
		return v, nil
	}
	return &ext
}
func (e *CommitmentHistoryExposure) ComposeExtension(base botruntime.Extension) *botruntime.Extension {
	return e.owner.compose(base)
}
func (e *CommitmentTimelineExposure) ComposeExtension(base botruntime.Extension) *botruntime.Extension {
	return e.owner.compose(base)
}
