package app

import (
	"context"
	"database/sql"
	"sync/atomic"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

// These immutable, validated snapshots are published by the committed settings
// callback. They are only feature inputs: concrete stores still perform their
// own same-session readiness, scope and authorization checks.
type settingsSnapshot struct{ value atomic.Value }

func (s *settingsSnapshot) publish(v map[string]any) {
	next := make(map[string]any, len(v))
	for k, value := range v {
		next[k] = value
	}
	s.value.Store(next)
}
func (s *settingsSnapshot) enabled(key string) bool {
	v, _ := s.value.Load().(map[string]any)
	return v[key] == true
}

type memoryOwners struct {
	projections *projections.Coordinator
	workspace   *workingset.Service
	snapshot    *settingsSnapshot
}

func (a *Application) composeMemory(ctx context.Context, o Owners, settings map[string]any, collator *topiccollation.Collator) (*memoryOwners, error) {
	snapshot := new(settingsSnapshot)
	snapshot.publish(settings)
	options := assertionauthority.Options{DateParser: o.Dates, Compare: o.Compare}
	plans, err := commitments.New(o.Main, o.Authority, commitments.Options{DateParser: o.Dates, Compare: o.Compare})
	if err != nil {
		return nil, err
	}
	// Source CommitmentStorage and RelationStorage constructors initialize their
	// fixed schemas before any MemoryService operation. The Go constructors bind
	// owners only, so perform their actual authority-backed initialization here.
	if err = plans.Initialize(ctx); err != nil {
		return nil, err
	}
	relations, err := relationstorage.New(o.Main, o.Authority)
	if err != nil {
		return nil, err
	}
	if err = relations.Initialize(ctx); err != nil {
		return nil, err
	}
	knowledgeOwner, err := knowledge.New(o.Main, o.Authority)
	if err != nil {
		return nil, err
	}
	semanticStore, err := semantic.New(o.Main, o.Authority)
	if err != nil {
		return nil, err
	}
	projection, err := projections.New(ctx, o.Main, o.Authority, func() bool { return snapshot.enabled("memoryProjectionEnabled") }, projections.AssertionReader(o.Assertions, options), projections.Options{})
	if err != nil {
		return nil, err
	}
	commitmentsEnabled := func(c commitments.Context) (bool, error) {
		return !c.Simulation && snapshot.enabled("memoryCommitmentsEnabled"), nil
	}
	workspaceEnabled := func() bool {
		return snapshot.enabled("memoryWorkingSetEnabled") || snapshot.enabled("memoryEpisodesEnabled")
	}
	workspace, err := workingset.New(ctx, o.Main, o.Authority, workingset.Ports{Enabled: workspaceEnabled, ResolveCurrent: memoryservice.ResolveWorkingSet(plans, o.Assertions, commitmentsEnabled, options)})
	if err != nil {
		return nil, err
	}
	cold, review := settings["memoryColdAfterDays"].(float64)*86400000, settings["memoryReviewAfterDays"].(float64)*86400000
	if err = workspace.ConfigurePolicy(&cold, &review); err != nil {
		return nil, err
	}
	a.Maintenance, err = workingset.NewMaintenance(workingset.MaintenancePorts{
		Enabled: func() (enabled bool, err error) {
			err = o.Authority.Publish(context.Background(), o.Main, func(*sql.Conn) ([]byte, error) { enabled = workspaceEnabled(); return nil, nil }, func([]byte) error { return nil })
			return
		},
		DrainOneBatch: func() (bool, error) { return maintenanceBatch(context.Background(), o, workspace, workspaceEnabled) },
		Failed: func() {
			emit(a.options.Logs, observability.Event{Level: "info", Module: "memory", Code: "working_set_maintenance_unavailable", Message: "Working-set maintenance unavailable"})
		},
	}, 250, nil)
	if err != nil {
		return nil, err
	}
	semanticOptions := semantic.DefaultCoordinatorOptions()
	semanticOptions.Collator = collator
	a.Semantic, err = memoryservice.NewSemanticRuntime(memoryservice.SemanticRuntimeDependencies{
		Memory:  memoryservice.Dependencies{Assertions: o.Assertions, Commitments: plans, Relations: relations, Projections: projection, WorkingSet: workspace, Knowledge: knowledgeOwner, Semantic: semanticStore, CommitmentsEnabled: commitmentsEnabled, Authority: options},
		Sources: o.Sources, Main: o.Main, Authority: o.Authority, Models: a.models, Read: a.Engine.Read, Foreground: a.Engine.Runtime, Logs: a.options.Logs, Maintenance: a.Maintenance, Options: semanticOptions,
	})
	if err != nil {
		return nil, err
	}
	return &memoryOwners{projection, workspace, snapshot}, nil
}

// Mirrors MemoryWorkspace.maintenanceBatch: only queued work is selected, never
// old source messages or a periodic model sweep. The 40/250 values are source
// batch/timer values, not new protective limits. Drain rechecks actual authority.
func maintenanceBatch(ctx context.Context, o Owners, workspace *workingset.Service, enabled func() bool) (bool, error) {
	scene := ""
	pending := false
	read := func(selectScene bool) error {
		return o.Authority.Publish(ctx, o.Main, func(c *sql.Conn) ([]byte, error) {
			if !enabled() {
				return nil, nil
			}
			var exists int
			if err := c.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='memory_working_set_jobs'").Scan(&exists); err != nil {
				return nil, err
			}
			if exists == 0 {
				return nil, nil
			}
			if selectScene {
				err := c.QueryRowContext(ctx, "SELECT scene_id FROM memory_working_set_jobs WHERE json_extract(value,'$.lease') IS NULL OR json_extract(value,'$.lease.expiresAt')<=? ORDER BY rowid LIMIT 1", time.Now().UnixMilli()).Scan(&scene)
				if err != nil && err != sql.ErrNoRows {
					return nil, err
				}
			}
			err := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM memory_working_set_jobs LIMIT 1)").Scan(&pending)
			return nil, err
		}, func([]byte) error { return nil })
	}
	if err := read(true); err != nil {
		return false, err
	}
	if scene == "" {
		return pending, nil
	}
	if _, err := workspace.Drain(ctx, workingset.Context{SceneID: scene}, 40); err != nil {
		return false, err
	}
	pending = false
	if err := read(false); err != nil {
		return false, err
	}
	return pending, nil
}
