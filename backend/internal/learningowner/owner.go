// Package learningowner binds source learning to the real native domain owners.
// It neither replays persisted messages nor supplies permissive fallback ports.
package learningowner

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningbatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"sync/atomic"
	"time"
)

type Object = jsjson.Object
type Dependencies struct {
	// ManagedSceneChanges leaves scene cancellation to an application that owns
	// the source-ordered Experience, runtime, semantic and learning effects.
	ManagedSceneChanges bool

	Main       *sql.DB
	Authority  *recovery.Authority
	Assertions *assertions.Store
	Sources    *sourceevents.Store
	Memory     *memoryservice.Service
	Habits     *habitservice.Service
	Models     *providertransport.NativeModels
	Read       *modelread.Policy
	Foreground *botruntime.Runtime
	Logs       observability.Emitter
	Proposal   memoryservice.ProposalOptions
	Now        func() time.Time
	Options    learningruntime.Options
}
type Owner struct {
	d              Dependencies
	closed         atomic.Bool
	indexBootstrap bool
}
type Runtime struct {
	Owner       *Owner
	Runner      *learningbatch.Runner
	Coordinator *learningruntime.Coordinator
	detach      func()
}

func New(d Dependencies) (*Runtime, error) {
	if d.Main == nil || d.Authority == nil || d.Assertions == nil || d.Sources == nil || d.Memory == nil || d.Habits == nil || d.Models == nil || d.Read == nil || d.Foreground == nil || d.Logs == nil {
		return nil, errors.New("learning_owner_dependencies_required")
	}
	if e := d.Assertions.CheckReadBinding(d.Authority, d.Main, nil); e != nil {
		return nil, e
	}
	if e := d.Memory.CheckLearningBinding(d.Authority, d.Main); e != nil {
		return nil, e
	}
	if e := d.Habits.CheckLearningBinding(d.Authority, d.Main); e != nil {
		return nil, e
	}
	if e := d.Read.CheckBinding(d.Authority, d.Main); e != nil {
		return nil, e
	}
	if e := d.Sources.WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error {
		return d.Sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error { return scope.CheckBinding(d.Main, d.Authority) })
	}); e != nil {
		return nil, e
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	owner := &Owner{d: d}
	if e := owner.initializeIndex(context.Background()); e != nil {
		return nil, e
	}
	settings, e := owner.settings(context.Background())
	if e != nil {
		return nil, e
	}
	options := d.Options
	options.Settings = coordinatorSettings(settings)
	runner, e := learningbatch.New(learningbatch.Dependencies{Reads: owner, Memory: d.Memory, Habits: d.Habits, Policy: d.Read, Models: d.Models, Now: d.Now, Disposed: owner.closed.Load, Log: owner.log, Event: owner.flow})
	if e != nil {
		return nil, e
	}
	coordinator, e := learningruntime.New(owner, runner, options)
	if e != nil {
		return nil, e
	}
	extension := coordinator.Extension()
	if d.ManagedSceneChanges {
		extension.SceneChanged = nil
	}
	return &Runtime{owner, runner, coordinator, d.Foreground.AddExtension(extension)}, nil
}
func (r *Runtime) CloseAndWait(ctx context.Context) error {
	r.Owner.closed.Store(true)
	r.detach()
	return r.Coordinator.CloseAndWait(ctx)
}
func (r *Runtime) SettingsChanged(ctx context.Context) error {
	s, e := r.Owner.settings(ctx)
	if e != nil {
		return e
	}
	return r.Coordinator.SettingsChanged(coordinatorSettings(s))
}
func coordinatorSettings(s map[string]any) learningruntime.Settings {
	n := func(k string) int64 { v, _ := s[k].(float64); return int64(v) }
	return learningruntime.Settings{BatchMS: n("learningBatchMs"), MaxWaitMS: n("learningMaxWaitMs"), MinIntervalMS: n("learningMinIntervalMs"), QueueMaxMessages: n("learningQueueMaxMessages"), QueueMaxTotalMessages: n("learningQueueMaxTotalMessages"), RetryAttempts: int(n("learningRetryAttempts")), RetryBaseMS: n("learningRetryBaseMs")}
}
func (o *Owner) settings(ctx context.Context) (out map[string]any, err error) {
	err = o.d.Authority.Publish(ctx, o.d.Main, func(conn *sql.Conn) ([]byte, error) {
		var raw string
		e := conn.QueryRowContext(ctx, "SELECT value FROM config WHERE key='settings'").Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			raw = string(contract.DefaultSettingsInput())
		} else if e != nil {
			return nil, e
		}
		schemas, e := contract.SharedSchemas()
		if e != nil {
			return nil, e
		}
		out, e = schemas.Parse("Settings", []byte(raw))
		return nil, e
	}, func([]byte) error { return nil })
	return
}
func (o *Owner) Settings(ctx context.Context) (learningbatch.Settings, error) {
	s, e := o.settings(ctx)
	if e != nil {
		return learningbatch.Settings{}, e
	}
	p := o.d.Proposal
	p.GraphAutoAdmission = s["memoryGraphAutoAdmissionEnabled"] == true
	p.KnowledgeAutoAdmission = s["memoryKnowledgeAutoAdmissionEnabled"] == true
	timezone, _ := s["timezone"].(string)
	return learningbatch.Settings{Timezone: timezone, GraphEnabled: s["memoryGraphEnabled"] == true, CommitmentsEnabled: s["memoryCommitmentsEnabled"] == true, KnowledgeEnabled: s["memoryKnowledgeEnabled"] == true, Proposal: p}, nil
}
func (o *Owner) Enabled(ctx context.Context, scene string) (enabled bool, err error) {
	err = o.d.Assertions.Read(ctx, func(r *assertions.Reader) error {
		row, e := r.Scene(scene)
		enabled = get(row, "learningEnabled") == true
		return e
	})
	return
}
func (o *Owner) Recalled(ctx context.Context, scene, id string) (recalled bool, err error) {
	err = o.d.Assertions.Read(ctx, func(r *assertions.Reader) error { var e error; recalled, e = r.Recalled(scene, id); return e })
	return
}
func (o *Owner) CurrentSource(ctx context.Context, m *Object) (bool, error) {
	return o.d.Sources.CurrentRuntimeSnapshot(ctx, m)
}
func (o *Owner) ForegroundBusy() bool { return o.d.Foreground.ActiveTasks() > 0 }
func (o *Owner) log(level, event string) {
	_ = o.d.Logs.Emit(observability.Event{Level: level, Module: "learning", Code: event, Message: event})
}
func (o *Owner) Event(e learningruntime.Event) {
	_ = o.d.Logs.Emit(observability.Event{Level: "info", Module: "learning", Code: e.Stage, Message: e.Stage, SceneID: e.SceneID, TraceID: e.TraceID, Stage: e.Stage, Fields: map[string]any{"status": e.Status, "reason": e.Reason, "nextStep": e.NextStep, "traceId": e.TraceID, "batchId": e.BatchID, "parentTraceId": e.ParentTraceID, "linkedTraceIds": e.LinkedTraceIDs, "attempt": e.Attempt, "inputCount": e.InputCount, "queueDepth": e.QueueDepth, "retryDelayMs": e.RetryDelayMS, "unconfirmedCandidateCount": e.UnconfirmedCandidateCount, "candidateCount": e.Totals.CandidateCount, "savedCount": e.Totals.SavedCount, "rejectedCount": e.Totals.RejectedCount, "ignoredCount": e.Totals.IgnoredCount}})
}
func (o *Owner) Targets(ctx context.Context, scene string, candidates, semantic []*Object, evidence []any) (learningbatch.TargetScope, error) {
	v, e := o.d.Memory.ReconciliationTargets(ctx, obj("sceneId", scene, "simulation", false, "botUserId", "bot", "sourceEvidence", evidence), candidates, semantic)
	return learningbatch.TargetScope{Targets: v.Targets, Coverage: v.Coverage, Limits: v.Limits}, e
}
func get(o *Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func str(o *Object, k string) string { s, _ := get(o, k).(string); return s }
func obj(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func arr(v any) []any { a, _ := v.([]any); return a }

var _ learningbatch.Reads = (*Owner)(nil)
var _ learningruntime.Owner = (*Owner)(nil)

func (o *Owner) flow(e learningbatch.FlowEvent) {
	level := "info"
	if e.Status == "failed" {
		level = "error"
	} else if e.Status == "partial" || e.Status == "cancelled" || e.Status == "cancel_requested" {
		level = "warn"
	}
	_ = o.d.Logs.Emit(observability.Event{Level: level, Module: "flow", Code: "flow." + e.Stage + "." + e.Status, Message: e.Stage, TraceID: e.TraceID, SceneID: e.SceneID, Stage: e.Stage, DurationMS: e.DurationMS, Fields: map[string]any{"status": e.Status, "reason": e.Reason, "nextStep": e.NextStep, "role": e.Role, "batchId": e.BatchID, "parentTraceId": e.ParentTraceID, "spanId": e.SpanID, "parentSpanId": e.ParentSpanID, "linkedTraceIds": e.LinkedTraceIDs, "counts": e.Counts}})
}
