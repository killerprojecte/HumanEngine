package contextuallearning

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningbatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

type Object = jsjson.Object
type ParentReference = memoryservice.Reference
type MemorySnapshot struct {
	ID            string `json:"id"`
	Version       int64  `json:"version"`
	DeletionEpoch int64  `json:"deletionEpoch"`
	Deleted       bool   `json:"deleted"`
}
type EntitySnapshot struct {
	EntityID   string          `json:"entityId"`
	BackingRef ParentReference `json:"backingRef"`
}
type CommitmentSnapshot struct {
	ID              string `json:"id"`
	Version         int64  `json:"version"`
	DeletionEpoch   int64  `json:"deletionEpoch"`
	MutationVersion int64  `json:"mutationVersion"`
}
type KnowledgeEdgeSnapshot struct {
	ID      string          `json:"id"`
	Version int64           `json:"version"`
	Backing ParentReference `json:"backing"`
}
type Settings struct {
	LearningEnabled, GraphEnabled, GraphAnswerEnabled, CommitmentsEnabled, KnowledgeEnabled bool
	// Captured for diagnostics. Source validity checks current feature flags, not
	// every unrelated settings edit. This is not extra invalidation policy.
	Revision string
}
type MemoryState struct {
	Version, DeletionEpoch int64
	Deleted, Visible       bool
}

// Guard must use the supplied writer session when nonnil, never reenter a root
// publication. Nil means a normal current read. Exposure owners mint these;
// model arguments never contain them. Missing domain guards fail closed.
type Guard func(context.Context, *recovery.WriteSession) (bool, error)
type ReadGuard struct {
	Identity string
	Valid    Guard
}
type Submission struct {
	SceneID    string
	Simulation bool
	// Nil means unavailable. A nonnil empty slice is an authoritative empty snapshot.
	SourceSnapshots   []*Object
	Input             *Object
	Memories          []MemorySnapshot
	Entities          []EntitySnapshot
	EntityValid       Guard
	Commitments       []CommitmentSnapshot
	CommitmentValid   Guard
	Read              *ReadGuard
	KnowledgeEntities []EntitySnapshot
	KnowledgeValid    Guard
	KnowledgeEdges    []KnowledgeEdgeSnapshot
}
type CommitRequest struct {
	SceneID           string
	Candidate         *Object
	Evidence          []*Object
	Parents           []ParentReference
	Entities          []EntitySnapshot
	Commitments       []CommitmentSnapshot
	KnowledgeEntities []EntitySnapshot
	KnowledgeEdges    []KnowledgeEdgeSnapshot
	CapturedSettings  Settings
	// Commit MUST recheck Validate inside its actual durable writer boundary.
	Validate Guard
}
type Ports struct {
	State          func(context.Context, string, *recovery.WriteSession) (Settings, error)
	Preview        func(string) ([]*Object, error)
	ForegroundBusy func() bool
	Registered     func(context.Context, string, string) (bool, error)
	CurrentSource  func(context.Context, *Object, *recovery.WriteSession) (bool, error)
	Recalled       func(context.Context, string, string, *recovery.WriteSession) (bool, error)
	MemoryStates   func(context.Context, string, []string, *recovery.WriteSession) (map[string]MemoryState, error)
	Compare        func(string, string) (int, error)
	Commit         func(context.Context, CommitRequest) (memoryservice.ProposalResult, error)
	OnEvent        func(Event)
}
type Proposals struct {
	Queue         *Queue
	ports         Ports
	parser        *learningbatch.CandidateParser
	mu            sync.Mutex
	closed        bool
	admissions    int
	admissionIdle chan struct{}
}

func NewProposals(p Ports) (*Proposals, error) {
	if p.State == nil || p.Preview == nil || p.ForegroundBusy == nil || p.Registered == nil || p.CurrentSource == nil || p.Recalled == nil || p.MemoryStates == nil || p.Compare == nil || p.Commit == nil {
		return nil, errors.New("contextual_proposal_ports_required")
	}
	parser, e := learningbatch.NewCandidateParser()
	if e != nil {
		return nil, e
	}
	q, e := NewQueue(QueueOptions{ForegroundBusy: p.ForegroundBusy, OnEvent: p.OnEvent})
	if e != nil {
		return nil, e
	}
	idle := make(chan struct{})
	close(idle)
	return &Proposals{Queue: q, ports: p, parser: parser, admissionIdle: idle}, nil
}
func (p *Proposals) CancelScene(scene string)    { p.Queue.CancelScene(scene) }
func (p *Proposals) Recall(scene, source string) { p.Queue.Recall(scene, source) }
func (p *Proposals) Dispose()                    { p.mu.Lock(); p.closed = true; p.mu.Unlock(); p.Queue.Dispose() }
func (p *Proposals) Resume()                     { p.Queue.Resume() }
func (p *Proposals) CloseAndWait(ctx context.Context) error {
	p.Dispose()
	p.mu.Lock()
	idle := p.admissionIdle
	p.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.Queue.CloseAndWait(ctx)
}
func (p *Proposals) WhenIdle(ctx context.Context) error { return p.Queue.WhenIdle(ctx) }
func (p *Proposals) Stats() Stats                       { return p.Queue.Stats() }
func get(o *Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func str(o *Object, k string) string { s, _ := get(o, k).(string); return s }
func arr(v any) []any                { a, _ := v.([]any); return a }
func obj(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// SnapshotJSON retains WTF-8 IDs and Number semantics instead of passing them
// through encoding/json's Unicode replacement behavior.
func SnapshotJSON(v any) (any, error) {
	ref := func(r ParentReference) *Object {
		return obj("id", r.ID, "version", float64(r.Version), "deletionEpoch", float64(r.DeletionEpoch))
	}
	out := []any{}
	switch values := v.(type) {
	case []MemorySnapshot:
		for _, m := range values {
			out = append(out, obj("id", m.ID, "version", float64(m.Version), "deletionEpoch", float64(m.DeletionEpoch), "deleted", m.Deleted))
		}
	case []ParentReference:
		for _, r := range values {
			out = append(out, ref(r))
		}
	case []EntitySnapshot:
		for _, r := range values {
			out = append(out, obj("entityId", r.EntityID, "backingRef", ref(r.BackingRef)))
		}
	case []CommitmentSnapshot:
		for _, r := range values {
			out = append(out, obj("id", r.ID, "version", float64(r.Version), "deletionEpoch", float64(r.DeletionEpoch), "mutationVersion", float64(r.MutationVersion)))
		}
	case []KnowledgeEdgeSnapshot:
		for _, r := range values {
			out = append(out, obj("id", r.ID, "version", float64(r.Version), "backing", ref(r.Backing)))
		}
	default:
		return nil, errors.New("contextual_snapshot_type_required")
	}
	return out, nil
}
func cloneObject(v *Object) (*Object, error) {
	raw, e := jsjson.Stringify(v)
	if e != nil {
		return nil, e
	}
	out, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	o, ok := out.(*Object)
	if !ok {
		return nil, errors.New("contextual_object_required")
	}
	return o, nil
}
func safeGuard(g Guard, ctx context.Context, ws *recovery.WriteSession) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if g == nil {
		return true
	}
	v, e := g(ctx, ws)
	return e == nil && v
}
func (p *Proposals) Submit(ctx context.Context, in Submission) (EnqueueResult, error) {
	reject := func(reason string) (EnqueueResult, error) {
		return EnqueueResult{Status: "rejected", Reason: reason}, nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return reject("disposed")
	}
	if p.admissions == 0 {
		p.admissionIdle = make(chan struct{})
	}
	p.admissions++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.admissions--
		if p.admissions == 0 {
			close(p.admissionIdle)
		}
		p.mu.Unlock()
	}()
	if in.Simulation {
		return reject("simulation_does_not_learn")
	}
	if ctx.Err() != nil {
		return reject("request_cancelled")
	}
	settings, e := p.ports.State(ctx, in.SceneID, nil)
	if e != nil {
		return EnqueueResult{}, e
	}
	if !settings.LearningEnabled {
		return reject("learning_disabled")
	}
	input, e := cloneObject(in.Input)
	if e != nil {
		return EnqueueResult{}, e
	}
	candidate, e := p.parser.ParseContextual(input, learningbatch.Settings{GraphEnabled: settings.GraphEnabled, CommitmentsEnabled: settings.CommitmentsEnabled, KnowledgeEnabled: settings.KnowledgeEnabled})
	if e != nil {
		return EnqueueResult{}, e
	}
	// Copy all caller-owned slices and read guard fields before enqueueing.
	in.Memories = append([]MemorySnapshot{}, in.Memories...)
	in.Entities = append([]EntitySnapshot{}, in.Entities...)
	in.Commitments = append([]CommitmentSnapshot{}, in.Commitments...)
	in.KnowledgeEntities = append([]EntitySnapshot{}, in.KnowledgeEntities...)
	in.KnowledgeEdges = append([]KnowledgeEdgeSnapshot{}, in.KnowledgeEdges...)
	if in.Read != nil {
		r := *in.Read
		in.Read = &r
	}
	knowledge := len(arr(get(candidate, "knowledge"))) > 0
	commitment, _ := get(candidate, "commitment").(*Object)
	if in.KnowledgeValid != nil && !knowledge {
		return reject("knowledge_state_not_freeform_learning_authority")
	}
	if (len(in.KnowledgeEntities) > 0 || len(in.KnowledgeEdges) > 0) && in.KnowledgeValid == nil {
		return reject("knowledge_authority_unavailable")
	}
	if in.CommitmentValid != nil && (commitment == nil || str(commitment, "kind") == "propose") {
		return reject("commitment_state_not_freeform_learning_authority")
	}
	if len(in.Commitments) > 0 && in.CommitmentValid == nil {
		return reject("commitment_authority_unavailable")
	}
	if len(in.Entities) > 0 && in.EntityValid == nil {
		return reject("entity_authority_unavailable")
	}
	var compareError error
	sort.SliceStable(in.Entities, func(i, j int) bool {
		c, e := p.ports.Compare(in.Entities[i].EntityID, in.Entities[j].EntityID)
		compareError = errors.Join(compareError, e)
		return c < 0
	})
	sort.SliceStable(in.Memories, func(i, j int) bool {
		c, e := p.ports.Compare(in.Memories[i].ID, in.Memories[j].ID)
		compareError = errors.Join(compareError, e)
		return c < 0
	})
	if compareError != nil {
		return EnqueueResult{}, compareError
	}
	sourceIDs := []string{}
	for _, id := range arr(get(candidate, "sourceMessageIds")) {
		s, ok := id.(string)
		if !ok {
			return reject("invalid")
		}
		sourceIDs = append(sourceIDs, s)
	}
	sourceIDs, e = canonicalSources(sourceIDs)
	if e != nil {
		return reject("invalid")
	}
	wanted := map[string]bool{}
	for _, id := range sourceIDs {
		wanted[id] = true
	}
	messages := in.SourceSnapshots
	if messages == nil {
		messages, e = p.ports.Preview(in.SceneID)
		if e != nil {
			return EnqueueResult{}, e
		}
	}
	evidence := []*Object{}
	sourceRefs := []any{}
	for _, m := range messages {
		if !wanted[str(m, "id")] {
			continue
		}
		copy, e := cloneObject(m)
		if e != nil {
			return EnqueueResult{}, e
		}
		evidence = append(evidence, copy)
		sourceRefs = append(sourceRefs, []any{get(copy, "id"), get(copy, "sourceEventRef")})
	}
	if in.SourceSnapshots == nil {
		for _, m := range evidence {
			// A captured source-event reference already proves that preview is not
			// an authoritative turn snapshot. Preserve source short-circuit order:
			// do not consult Registered (which can fail) when that ref is present.
			if get(m, "sourceEventRef") != nil {
				return reject("source_snapshot_unavailable")
			}
			registered, e := p.ports.Registered(ctx, in.SceneID, str(m, "id"))
			if e != nil {
				return EnqueueResult{}, e
			}
			if registered {
				return reject("source_snapshot_unavailable")
			}
		}
	}
	for _, m := range evidence {
		if get(m, "sourceEventRegistrationLimited") == true {
			return reject("source_event_registration_limited")
		}
	}
	parents := make([]ParentReference, len(in.Memories))
	ids := []string{}
	for i, m := range in.Memories {
		parents[i] = ParentReference{ID: m.ID, Version: m.Version, DeletionEpoch: m.DeletionEpoch}
		ids = append(ids, m.ID)
	}
	for _, ent := range in.Entities {
		ids = append(ids, ent.BackingRef.ID)
	}
	valid := func(current context.Context, ws *recovery.WriteSession) (bool, error) {
		if current.Err() != nil {
			return false, nil
		}
		s, e := p.ports.State(current, in.SceneID, ws)
		if e != nil {
			return false, e
		}
		if knowledge && !s.KnowledgeEnabled {
			return false, nil
		}
		if !safeGuard(in.KnowledgeValid, current, ws) {
			return false, nil
		}
		if in.Read != nil && (in.Read.Valid == nil || !safeGuard(in.Read.Valid, current, ws)) {
			return false, nil
		}
		if !s.LearningEnabled {
			return false, nil
		}
		if len(in.Entities) > 0 && (!s.GraphEnabled || !s.GraphAnswerEnabled) {
			return false, nil
		}
		if !safeGuard(in.CommitmentValid, current, ws) {
			return false, nil
		}
		if commitment != nil && !s.CommitmentsEnabled {
			return false, nil
		}
		if !safeGuard(in.EntityValid, current, ws) {
			return false, nil
		}
		if (len(arr(get(candidate, "relations"))) > 0 || len(in.Entities) > 0) && !s.GraphEnabled {
			return false, nil
		}
		for _, id := range sourceIDs {
			found := false
			for _, m := range evidence {
				if str(m, "id") != id || str(m, "sceneId") != in.SceneID || str(m, "origin") != "live" || str(m, "status") != "available" || str(m, "senderId") == "bot" {
					continue
				}
				ok, e := p.ports.CurrentSource(current, m, ws)
				if e != nil {
					return false, e
				}
				if ok {
					found = true
					break
				}
			}
			if !found {
				return false, nil
			}
			recalled, e := p.ports.Recalled(current, in.SceneID, id, ws)
			if e != nil || recalled {
				return false, e
			}
		}
		states, e := p.ports.MemoryStates(current, in.SceneID, ids, ws)
		if e != nil {
			return false, e
		}
		exact := func(r ParentReference) bool {
			s, ok := states[r.ID]
			return ok && s.Visible && !s.Deleted && s.Version == r.Version && s.DeletionEpoch == r.DeletionEpoch
		}
		for _, ent := range in.Entities {
			if !exact(ent.BackingRef) {
				return false, nil
			}
		}
		for _, r := range parents {
			if !exact(r) {
				return false, nil
			}
		}
		return true, nil
	}
	keyObject := obj("candidate", candidate, "sourceIds", toAny(sourceIDs), "sourceRefs", sourceRefs)
	for _, field := range []struct {
		name string
		v    any
	}{{"parentStates", in.Memories}, {"entityStates", in.Entities}, {"commitmentStates", in.Commitments}, {"knowledgeStates", in.KnowledgeEntities}, {"knowledgeEdges", in.KnowledgeEdges}} {
		v, e := SnapshotJSON(field.v)
		if e != nil {
			return EnqueueResult{}, e
		}
		_ = keyObject.Set(field.name, v)
	}
	if in.Read != nil {
		_ = keyObject.Set("readPolicyIdentity", in.Read.Identity)
	}
	raw, e := jsjson.Stringify(keyObject)
	if e != nil {
		return EnqueueResult{}, e
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	turnRaw, _ := jsjson.Stringify([]any{in.SceneID, key})
	turnID := fmt.Sprintf("contextual:%x", sha256.Sum256([]byte(turnRaw)))
	request := CommitRequest{SceneID: in.SceneID, Candidate: candidate, Evidence: evidence, Parents: parents, Entities: in.Entities, Commitments: in.Commitments, KnowledgeEntities: in.KnowledgeEntities, KnowledgeEdges: in.KnowledgeEdges, CapturedSettings: settings, Validate: valid}
	return p.Queue.Enqueue(Job{SceneID: in.SceneID, Key: key, SourceIDs: sourceIDs, Validate: func() bool { return safeGuard(valid, context.Background(), nil) }, Committed: func(v any) bool {
		r, ok := v.(memoryservice.ProposalResult)
		return ok && r.Status == "saved" && str(r.Memory, "id") != ""
	}, Run: func(current context.Context) (any, error) {
		if !safeGuard(valid, current, nil) {
			return nil, nil
		}
		current = providertransport.WithBudgetContext(current, turnID, "realtime")
		current, e := scheduler.WithModelPriority(current, 1)
		if e != nil {
			return nil, e
		}
		return p.ports.Commit(current, request)
	}}), nil
}
func toAny(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}
