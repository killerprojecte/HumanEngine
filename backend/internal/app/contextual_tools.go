package app

import (
	"context"
	_ "embed"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contextuallearning"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

//go:embed testdata/contextual-tools-source.json
var contextualToolsSource string

func contextualToolSchema(snapshot *settingsSnapshot) *jsjson.Object {
	flags := 0
	for i, key := range []string{"memoryGraphEnabled", "memoryCommitmentsEnabled", "memoryKnowledgeEnabled"} {
		if snapshot.enabled(key) {
			flags |= 1 << i
		}
	}
	raw, err := jsjson.Parse(contextualToolsSource)
	if err != nil {
		panic(err)
	}
	definitions, _ := raw.(*jsjson.Object).Get("definitions")
	return definitions.([]any)[flags].(*jsjson.Object)
}

// Source advertises learning.propose after identity and before state history.
// The runtime applies its current-scene learning filter at the planning boundary.
func (a *Application) registerContextualTool(memory *memoryOwners) {
	a.Engine.Runtime.AddExtension(&botruntime.Extension{
		ToolsForTurn: func(*botruntime.TurnContext) []*jsjson.Object {
			return []*jsjson.Object{contextualToolSchema(memory.snapshot)}
		},
		Execute: func(ctx context.Context, name string, input any, c *botruntime.TurnContext, done func(any, error)) {
			if name != "learning.propose" {
				done(nil, errors.New("runtime_unknown_tool"))
				return
			}
			value, err := a.proposeContextualTool(ctx, c, input)
			done(value, err)
		},
	})
}

// Capture an immutable guard but borrow only the current caller's authority.
// Queue commit validation supplies its writer; never retain the turn or session.
func (a *Application) contextualGuard(check func(botruntime.ReadAuthority) (bool, error)) contextuallearning.Guard {
	if check == nil {
		return nil
	}
	return func(ctx context.Context, session *recovery.WriteSession) (valid bool, err error) {
		sources := a.Engine.Sources
		if session != nil {
			sources = sources.WithSession(session)
		}
		err = sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { var e error; valid, e = check(reads); return e })
		return
	}
}

func (a *Application) proposeContextualTool(ctx context.Context, c *botruntime.TurnContext, input any) (any, error) {
	reject := func(reason string) (any, error) { return memoryToolObject("status", "rejected", "reason", reason), nil }
	// learning.propose itself is not a gateModelTool memory read. The source's
	// early identity guard precedes abort/validation checks.
	if a.IdentityExposure.CaptureGuard(c) != nil {
		return reject("identity_read_not_learning_authority")
	}
	valid, err := c.ValidateMemoryContext()
	if err != nil {
		return nil, err
	}
	if !valid {
		return reject("context_ineligible")
	}
	if a.IdentityExposure.CaptureGuard(c) != nil {
		return reject("identity_read_not_learning_authority")
	}
	// Semantic/workspace/relation/knowledge exposures are not composed here. No
	// synthetic exposure guard or implied learning capability replaces those owners.
	if a.StateExposures.historical.CaptureGuard(c) != nil || a.StateExposures.history.CaptureGuard(c) != nil || a.StateExposures.timeline.CaptureGuard(c) != nil {
		return reject("historical_read_not_learning_authority")
	}
	args, ok := input.(*jsjson.Object)
	if !ok {
		return nil, errors.New("contextual_learning_input_required")
	}
	submission := contextuallearning.Submission{SceneID: c.Scene.ID, Simulation: c.Mode == "simulation", Input: args}
	if c.HasSourceSnapshots() {
		submission.SourceSnapshots = c.Sources()
	}
	for _, ref := range a.MemoryExposure.ReadExposed(c) {
		submission.Memories = append(submission.Memories, contextuallearning.MemorySnapshot{ID: ref.ID, Version: ref.Version, DeletionEpoch: ref.DeletionEpoch, Deleted: ref.Deleted})
	}
	refs, err := a.StateExposures.current.ReadExposedRefs(ctx, c)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		submission.Commitments = append(submission.Commitments, contextuallearning.CommitmentSnapshot{ID: ref.ID, Version: int64(ref.Version), DeletionEpoch: int64(ref.DeletionEpoch), MutationVersion: int64(ref.MutationVersion)})
	}
	if guard := a.StateExposures.current.CaptureGuard(c); guard != nil {
		submission.CommitmentValid = a.contextualGuard(guard.Check)
	}
	identity, _, present := a.Engine.TurnRead.CaptureGuard(c)
	if present {
		check, ok := a.Engine.TurnRead.CaptureScopedGuard(c)
		if !ok {
			return nil, errors.New("contextual_read_guard_missing")
		}
		submission.Read = &contextuallearning.ReadGuard{Identity: identity, Valid: a.contextualGuard(check)}
	}
	result, err := a.Contextual.Submit(ctx, submission)
	if err != nil {
		return nil, err
	}
	out := memoryToolObject("status", result.Status)
	if result.JobID != "" {
		out.Set("jobId", result.JobID)
	}
	if result.Reason != "" {
		out.Set("reason", result.Reason)
	}
	return out, nil
}
