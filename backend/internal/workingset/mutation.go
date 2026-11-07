package workingset

import (
	"bytes"
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// MutationHooks is a trusted authoritative lifecycle adapter. Unlike Service
// methods it never opens a transaction. Construct it from the active owner.
// It cannot be used as a model tool or substitute for WorkspaceAuthority.
type MutationHooks struct {
	service *Service
	ports   MutationPorts
}
type MutationPorts struct {
	ResolveCommitted        func(*recovery.WriteSession, Context, []Reference) ([]Snapshot, error)
	AffectedAssertionScenes func(*recovery.WriteSession, string, *string, int) ([]string, error)
}

func (s *Service) Mutations(ports MutationPorts) *MutationHooks { return &MutationHooks{s, ports} }

type MutationResult struct {
	Queued      int    `json:"queued"`
	Unavailable int    `json:"unavailable"`
	Coverage    string `json:"coverage"`
}
type EditedResult struct {
	Queued             int    `json:"queued"`
	Unavailable        int    `json:"unavailable"`
	Coverage           string `json:"coverage"`
	InvalidatedTargets int    `json:"invalidatedTargets"`
}

func (m *MutationHooks) ready() error {
	if m.service.session == nil {
		return errors.New("working_set_mutation_transaction_required")
	}
	return m.service.ready()
}
func mutationReferences(input []Reference) ([]Reference, error) {
	if len(input) > 40 {
		return nil, errors.New("working_set_mutation_limit")
	}
	out := []Reference{}
	seen := map[string]int{}
	for _, raw := range input {
		r, e := clone(raw)
		if e != nil {
			return nil, e
		}
		key := EntityKey(r)
		if i, ok := seen[key]; ok {
			if ReferenceKey(out[i]) != ReferenceKey(r) {
				return nil, errors.New("working_set_mutation_conflict")
			}
			out[i] = r
		} else {
			seen[key] = len(out)
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *MutationHooks) enqueue(ctx Context, refs []Reference) (MutationResult, error) {
	s := m.service
	out := MutationResult{Coverage: "disabled"}
	if ctx.Simulation || !s.ports.Enabled() {
		return out, nil
	}
	out.Coverage = "complete"
	if len(refs) == 0 {
		return out, nil
	}
	if m.ports.ResolveCommitted == nil {
		return out, errors.New("working_set_mutation_authority_invalid")
	}
	wanted := map[string]bool{}
	for _, r := range refs {
		wanted[ReferenceKey(r)] = true
	}
	resolved, e := m.ports.ResolveCommitted(s.session, ctx, refs)
	if e != nil {
		return out, e
	}
	if len(resolved) > 40 {
		return out, errors.New("working_set_mutation_authority_invalid")
	}
	snapshots := []Snapshot{}
	seen := map[string]bool{}
	for _, raw := range resolved {
		snapshot, e := clone(raw)
		if e != nil {
			return out, e
		}
		key := ReferenceKey(snapshot.Ref)
		if snapshot.SceneID != ctx.SceneID || !wanted[key] || seen[key] {
			return out, errors.New("working_set_mutation_authority_invalid")
		}
		seen[key] = true
		snapshots = append(snapshots, snapshot)
	}
	now := s.ports.Now()
	if !safe(now) {
		return out, errors.New("working_set_mutation_clock_invalid")
	}
	for _, snapshot := range snapshots {
		item, e := s.Storage().Item(ctx.SceneID, snapshot.Ref)
		if e != nil {
			return out, e
		}
		if item != nil && SameSelection(item.Snapshot.Selection(), snapshot.Selection()) {
			continue
		}
		job, e := s.Storage().Job(ctx.SceneID, snapshot.Ref)
		if e != nil {
			return out, e
		}
		if job != nil && SameSelection(job.Snapshot.Selection(), snapshot.Selection()) {
			continue
		}
		if e = s.write(); e != nil {
			return out, e
		}
		token, e := s.ports.Token()
		if e != nil {
			return out, e
		}
		if e = s.Storage().PutJob(Job{1, snapshot, token, now, nil}); e != nil {
			return out, e
		}
		out.Queued++
	}
	if e = s.ready(); e != nil {
		return out, e
	}
	out.Unavailable = len(wanted) - len(snapshots)
	if out.Unavailable > 0 {
		out.Coverage = "limited"
	}
	return out, nil
}
func (m *MutationHooks) Committed(ctx Context, input []Reference) (MutationResult, error) {
	if e := m.ready(); e != nil {
		return MutationResult{}, e
	}
	if ctx.SceneID == "" {
		return MutationResult{}, errors.New("working_set_mutation_context_invalid")
	}
	refs, e := mutationReferences(input)
	if e != nil {
		return MutationResult{}, e
	}
	return m.enqueue(ctx, refs)
}
func (m *MutationHooks) SourceEdited(ctx Context, input []Reference) (EditedResult, error) {
	if e := m.ready(); e != nil {
		return EditedResult{}, e
	}
	if ctx.SceneID == "" {
		return EditedResult{}, errors.New("working_set_mutation_context_invalid")
	}
	refs, e := mutationReferences(input)
	if e != nil {
		return EditedResult{}, e
	}
	if ctx.Simulation {
		return EditedResult{Coverage: "disabled"}, nil
	}
	if len(refs) > 0 {
		entities := []Entity{}
		for _, r := range refs {
			entities = append(entities, entity(r))
		}
		if e = m.service.Invalidate(context.Background(), ctx.SceneID, entities); e != nil {
			return EditedResult{}, e
		}
	}
	out, e := m.enqueue(ctx, refs)
	return EditedResult{out.Queued, out.Unavailable, out.Coverage, len(refs)}, e
}

type ScenesResult struct {
	Scenes int `json:"scenes"`
}

func (m *MutationHooks) TerminalAssertionDeleted(id string) (ScenesResult, error) {
	return m.invalidateAcrossScenes(id, true)
}
func (m *MutationHooks) InvalidateAssertion(id string) (ScenesResult, error) {
	return m.invalidateAcrossScenes(id, false)
}
func (m *MutationHooks) invalidateAcrossScenes(id string, terminal bool) (ScenesResult, error) {
	out := ScenesResult{}
	if e := m.ready(); e != nil {
		return out, e
	}
	if !explicitID(id) {
		return out, errors.New("working_set_mutation_assertion_invalid")
	}
	var after *string
	for {
		var page []string
		var e error
		if m.ports.AffectedAssertionScenes != nil {
			page, e = m.ports.AffectedAssertionScenes(m.service.session, id, after, 40)
		} else {
			page, e = m.service.Storage().AffectedAssertionScenes(id, after, 40)
		}
		if e != nil {
			return out, e
		}
		if len(page) > 40 {
			return out, errors.New("working_set_mutation_scene_page_invalid")
		}
		for i, scene := range page {
			if !explicitID(scene) {
				return out, errors.New("working_set_mutation_scene_page_invalid")
			}
			var previous *string
			if i > 0 {
				previous = &page[i-1]
			} else {
				previous = after
			}
			if previous != nil {
				a, _ := jsjson.UTF8Text(scene)
				b, _ := jsjson.UTF8Text(*previous)
				if bytes.Compare([]byte(a), []byte(b)) <= 0 {
					return out, errors.New("working_set_mutation_scene_page_invalid")
				}
			}
		}
		for _, scene := range page {
			refs := []Entity{{"assertion", id}}
			if terminal {
				e = m.service.TerminalInvalidate(context.Background(), scene, refs)
			} else {
				e = m.service.Invalidate(context.Background(), scene, refs)
			}
			if e != nil {
				return out, e
			}
			out.Scenes++
		}
		if len(page) < 40 {
			break
		}
		last := page[len(page)-1]
		after = &last
	}
	return out, m.service.ready()
}
