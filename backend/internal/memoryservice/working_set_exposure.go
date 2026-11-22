package memoryservice

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

type WorkingSetToolQuery struct {
	PersonID    string
	IncludeCold bool
}

type workingSetExposurePort struct{ memory *ExposurePort }

func NewWorkingSetExposure(memory *ExposurePort, tracker *WorkspaceSelectionTracker) (*StateExposure, error) {
	return NewStateExposure(workingSetExposurePort{memory}, func(ctx context.Context, c *botruntime.TurnContext, values []*Object) error {
		var refs []Reference
		for _, raw := range workingSetItems(values[0]) {
			item := object(raw)
			if text(item, "kind") == "assertion" {
				ref := object(value(object(value(item, "snapshot")), "ref"))
				refs = append(refs, Reference{ID: text(ref, "id"), Version: int64(episodeNumber(ref, "version")), DeletionEpoch: int64(episodeNumber(ref, "deletionEpoch"))})
			}
		}
		return tracker.Capture(ctx, c, refs)
	})
}

func workingSetItems(state *Object) []any {
	return epArr(value(object(value(state, "page")), "items"))
}

func (p workingSetExposurePort) borrow(ctx context.Context, use func(sourceevents.RuntimeReadScope) error) error {
	return p.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		return p.memory.sources.UseRuntimeReadScope(reads, use)
	})
}

func workingSetAnswerPlans(settings *Object) bool {
	return truthy(value(settings, "memoryCommitmentsEnabled")) && truthy(value(settings, "memoryCommitmentsAnswerEnabled"))
}

func (p workingSetExposurePort) Query(ctx context.Context, c Context, input any) ([]*Object, error) {
	empty := []*Object{}
	var enabled, answerPlans bool
	settingsRead := &commitmentExposurePort{p.memory}
	err := p.borrow(ctx, func(scope sourceevents.RuntimeReadScope) error {
		settings, err := settingsRead.settings(scope)
		enabled = truthy(value(settings, workingSetFeature))
		answerPlans = workingSetAnswerPlans(settings)
		return err
	})
	if err != nil || !enabled {
		return empty, err
	}
	in := input.(WorkingSetToolQuery)
	open := "open"
	s := p.memory.memory
	candidates, err := s.prepareWorkspaceCandidates(ctx, EpisodesQuery{SceneID: c.SceneID, PersonID: in.PersonID}, false, answerPlans, &open)
	if err != nil {
		return nil, err
	}
	if err = s.refreshWorkspaceMetadata(ctx, c.SceneID, candidates.selections); err != nil {
		return nil, err
	}
	var state *Object
	err = p.borrow(ctx, func(scope sourceevents.RuntimeReadScope) error {
		settings, err := settingsRead.settings(scope)
		if err != nil || !truthy(value(settings, workingSetFeature)) || workingSetAnswerPlans(settings) != answerPlans {
			return err
		}
		view, err := s.d.WorkingSet.ViewRuntime(scope, workingset.Context{SceneID: c.SceneID}, candidates.selections, in.IncludeCold)
		if err == nil && view.Enabled {
			state = workingSetModelPage(candidates, view)
		}
		return err
	})
	if state == nil {
		return empty, err
	}
	return []*Object{state}, err
}

const workingSetPageLimit = 12

func workingSetModelPage(c workspaceCandidates, view workingset.ViewResult) *Object {
	rows := workingSetRows(c, view.Items)
	coverage := "complete"
	if c.assertionsCoverage == "limited" || c.plansCoverage == "limited" || view.Coverage == "limited" || len(rows) > workingSetPageLimit {
		coverage = "limited"
	}
	items := make([]any, 0, min(len(rows), workingSetPageLimit))
	for _, r := range rows[:min(len(rows), workingSetPageLimit)] {
		body := "snapshot"
		if r.access.Ref.Kind == "commitment" {
			body = "view"
		}
		items = append(items, epObj("kind", r.access.Ref.Kind, body, r.row, "accessibility", workingSetAccess(r.access)))
	}
	return epObj("stateKind", "working_set", "page", epObj("enabled", true, "items", items, "coverage", coverage,
		"omittedCold", float64(view.OmittedCold), "omittedEntries", float64(max(len(rows)-workingSetPageLimit, 0)),
		"interpretation", "accessibility_not_truth_or_absence"))
}

func (p workingSetExposurePort) Valid(ctx context.Context, c Context, values []*Object) (bool, error) {
	valid := false
	err := p.borrow(ctx, func(scope sourceevents.RuntimeReadScope) error {
		valid = p.valid(scope, c, values)
		return nil
	})
	return valid && err == nil, nil
}

func (p workingSetExposurePort) ValidScoped(reads botruntime.ReadAuthority, c Context, values []*Object) (bool, error) {
	valid := false
	err := p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		valid = p.valid(scope, c, values)
		return nil
	})
	return valid && err == nil, nil
}

type workingSetProof struct {
	selection    workingset.Selection
	access, body *Object
}

func workingSetProofs(scene string, state *Object) ([]workingSetProof, bool) {
	page := object(value(state, "page"))
	items, ok := value(page, "items").([]any)
	coverage := text(page, "coverage")
	if text(state, "stateKind") != "working_set" || value(page, "enabled") != true || !ok || len(items) > workingSetPageLimit || (coverage != "complete" && coverage != "limited") {
		return nil, false
	}
	proofs := make([]workingSetProof, 0, len(items))
	seen := make(map[workingset.Entity]struct{}, len(items))
	for _, raw := range items {
		item := object(raw)
		kind := text(item, "kind")
		access := object(value(item, "accessibility"))
		ref := object(value(access, "ref"))
		r := workingset.Reference{Kind: kind, ID: text(ref, "id"), Version: episodeNumber(ref, "version"), DeletionEpoch: episodeNumber(ref, "deletionEpoch")}
		var body *Object
		switch kind {
		case "assertion":
			body = object(value(item, "snapshot"))
			record := object(value(body, "record"))
			scope := object(value(record, "scope"))
			if text(scope, "kind") != "scene" || text(scope, "sceneId") != scene || text(record, "id") != r.ID {
				return nil, false
			}
		case "commitment":
			body = object(value(item, "view"))
			if text(body, "sceneId") != scene {
				return nil, false
			}
			mutation := episodeNumber(body, "mutationVersion")
			r.MutationVersion = &mutation
		default:
			return nil, false
		}
		br := object(value(body, "ref"))
		if !deepEqual(ref, workingSetRef(r)) || text(br, "id") != r.ID || !deepEqual(value(br, "version"), r.Version) || !deepEqual(value(br, "deletionEpoch"), r.DeletionEpoch) {
			return nil, false
		}
		key := workingset.Entity{Kind: kind, ID: r.ID}
		if _, ok := seen[key]; ok {
			return nil, false
		}
		seen[key] = struct{}{}
		proofs = append(proofs, workingSetProof{workingset.Selection{Ref: r, DependencyKey: text(access, "dependencyKey")}, access, body})
	}
	return proofs, true
}

func (p workingSetExposurePort) valid(scope sourceevents.RuntimeReadScope, c Context, values []*Object) bool {
	if len(values) != 1 {
		return false
	}
	proofs, ok := workingSetProofs(c.SceneID, values[0])
	if !ok {
		return false
	}
	settings, err := (&commitmentExposurePort{p.memory}).settings(scope)
	if err != nil || !truthy(value(settings, workingSetFeature)) {
		return false
	}
	d := p.memory.memory.d
	selections := make([]workingset.Selection, len(proofs))
	for i, proof := range proofs {
		selections[i] = proof.selection
	}
	wsCtx := workingset.Context{SceneID: c.SceneID}
	current, witness, err := d.WorkingSet.ViewRuntimeWithWitness(scope, wsCtx, selections)
	if err != nil || !current.Enabled || len(current.Items) != len(proofs) {
		return false
	}
	if len(proofs) == 0 {
		return true
	}
	metadata := make(map[workingset.Entity]workingset.ViewItem, len(current.Items))
	for _, item := range current.Items {
		metadata[workingset.Entity{Kind: item.Ref.Kind, ID: item.Ref.ID}] = item
	}
	for _, proof := range proofs {
		ref := proof.selection.Ref
		item, ok := metadata[workingset.Entity{Kind: ref.Kind, ID: ref.ID}]
		if !ok || !workingSetPolicyMatches(proof.access, item) ||
			(ref.Kind == "commitment" && !truthy(value(settings, "memoryCommitmentsEnabled"))) ||
			!deepEqual(witness[workingset.SelectionKey(proof.selection)], proof.body) {
			return false
		}
	}
	return true
}

func workingSetPolicyMatches(access *Object, current workingset.ViewItem) bool {
	return deepEqual(value(access, "ref"), workingSetRef(current.Ref)) &&
		value(access, "dependencyKey") == current.DependencyKey && value(access, "pinned") == current.Pinned &&
		value(access, "pinIntent") == current.PinIntent && deepEqual(value(access, "pinRevision"), current.PinRevision) &&
		value(access, "pinStatus") == current.PinStatus && value(access, "policyCoverage") == current.PolicyCoverage &&
		value(access, "protection") == workspaceNullable(current.Protection)
}

func (p workingSetExposurePort) SourceIDs(values []*Object) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, raw := range workingSetItems(values[0]) {
		item := object(raw)
		if text(item, "kind") == "assertion" {
			record := object(value(object(value(item, "snapshot")), "record"))
			for _, source := range epArr(value(record, "sources")) {
				add(text(object(source), "messageId"))
			}
		} else {
			for _, id := range commitmentViewSources(object(value(item, "view"))) {
				add(id)
			}
		}
	}
	return ids, nil
}
