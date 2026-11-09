package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryworkspace"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"sync"
	"time"
)

// ResolveWorkspace uses the same current assertion/commitment owner as the
// existing workspace service. The source caller splits its 40-item batches.
func (s *Service) ResolveWorkspace(ctx context.Context, c Context, refs []workingset.Reference) (out []workingset.Snapshot, err error) {
	err = s.d.Commitments.ReadCurrent(ctx, s.d.Assertions, s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
		resolver, e := memoryworkspace.NewResolver(read, s.d.Authority.DateParser)
		if e != nil {
			return e
		}
		out, e = resolver.Resolve(workingset.Context{SceneID: c.SceneID, Simulation: c.Simulation}, refs)
		return e
	})
	return
}

type workspaceSelectionState struct {
	operation   sync.Mutex
	initialized bool
	mu          sync.Mutex
	delivered   map[string]workingset.Selection
}
type WorkspaceSelectionTracker struct {
	memory  *Service
	enabled func() bool
	now     func() float64
}

func NewWorkspaceSelectionTracker(memory *Service, enabled func() bool, now func() float64) (*WorkspaceSelectionTracker, error) {
	if memory == nil || enabled == nil {
		return nil, errors.New("workspace_selection_owners_required")
	}
	if now == nil {
		now = func() float64 { return float64(time.Now().UnixMilli()) }
	}
	return &WorkspaceSelectionTracker{memory, enabled, now}, nil
}
func (t *WorkspaceSelectionTracker) turn(c *botruntime.TurnContext) *workspaceSelectionState {
	return c.OwnedExtensionState(t, func() any { return &workspaceSelectionState{delivered: map[string]workingset.Selection{}} }).(*workspaceSelectionState)
}
func (t *WorkspaceSelectionTracker) Capture(ctx context.Context, c *botruntime.TurnContext, refs []Reference) error {
	if !t.enabled() || c.Mode != "real" || exposureAborted(c) || len(refs) == 0 {
		return nil
	}
	state := t.turn(c)
	state.operation.Lock()
	defer state.operation.Unlock()
	state.mu.Lock()
	existing := state.initialized
	state.mu.Unlock()
	staged := map[string]workingset.Selection{}
	for start := 0; start < len(refs); start += 40 {
		end := min(start+40, len(refs))
		batch := make([]workingset.Reference, end-start)
		for i, ref := range refs[start:end] {
			batch[i] = workingset.Reference{Kind: "assertion", ID: ref.ID, Version: float64(ref.Version), DeletionEpoch: float64(ref.DeletionEpoch)}
		}
		snapshots, err := t.memory.ResolveWorkspace(ctx, exposureContext(c), batch)
		if err != nil {
			return err
		}
		if existing {
			state.mu.Lock()
			for _, snapshot := range snapshots {
				if _, ok := state.delivered[snapshot.Ref.ID]; !ok {
					state.delivered[snapshot.Ref.ID] = snapshot.Selection()
				}
			}
			state.mu.Unlock()
		} else {
			for _, snapshot := range snapshots {
				if _, ok := staged[snapshot.Ref.ID]; !ok {
					staged[snapshot.Ref.ID] = snapshot.Selection()
				}
			}
		}
	}
	if !existing {
		state.mu.Lock()
		state.delivered = staged
		state.initialized = true
		state.mu.Unlock()
	}
	return nil
}

// Selected accounts only assertion bodies retained in the final whole-page
// model handoff. Capture alone is not a model call, successful use, or truth.
func (t *WorkspaceSelectionTracker) Selected(ctx context.Context, c *botruntime.TurnContext, brief any) error {
	if !t.enabled() || c.Mode != "real" || exposureAborted(c) {
		return nil
	}
	ids := []string{}
	seen := map[string]bool{}
	add := func(v any) {
		id, ok := v.(string)
		if ok && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	evidence, _ := value(object(brief), "evidence").([]any)
	for _, raw := range evidence {
		receipt := object(raw)
		memories, _ := value(receipt, "memories").([]any)
		for _, raw := range memories {
			add(value(object(raw), "id"))
		}
		states, _ := value(receipt, "states").([]any)
		for _, raw := range states {
			page := object(value(object(raw), "page"))
			items, _ := value(page, "items").([]any)
			for _, raw := range items {
				record := object(raw)
				kind := text(record, "kind")
				if kind != "assertion" && kind != "reported_experience" {
					continue
				}
				snapshot := object(value(record, "snapshot"))
				ref := value(snapshot, "ref")
				if ref == nil {
					ref = value(record, "assertionRef")
				}
				add(value(object(ref), "id"))
			}
		}
	}
	state := t.turn(c)
	state.mu.Lock()
	selected := []workingset.Selection{}
	for _, id := range ids {
		if item, ok := state.delivered[id]; ok {
			selected = append(selected, item)
		}
	}
	state.mu.Unlock()
	for start := 0; start < len(selected); start += 40 {
		end := min(start+40, len(selected))
		_, err := t.memory.d.WorkingSet.RecordSelected(ctx, workingset.Context{SceneID: c.Scene.ID}, workingset.SelectedInput{TurnID: c.TurnID, SelectedAt: t.now(), Selections: selected[start:end]})
		if err != nil {
			return err
		}
	}
	return nil
}
func (t *WorkspaceSelectionTracker) ComposeExtension(base botruntime.Extension) *botruntime.Extension {
	ext := base
	ext.ContextSelected = func(c *botruntime.TurnContext, brief *Object) error {
		if err := t.Selected(context.Background(), c, brief); err != nil {
			return err
		}
		if base.ContextSelected != nil {
			return base.ContextSelected(c, brief)
		}
		return nil
	}
	return &ext
}
