package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/experienceview"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryworkspace"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

type episodeExposurePort struct{ memory *ExposurePort }

func NewEpisodeExposure(memory *ExposurePort, tracker *WorkspaceSelectionTracker) (*StateExposure, error) {
	return NewStateExposure(episodeExposurePort{memory}, func(ctx context.Context, c *botruntime.TurnContext, values []*Object) error {
		var refs []Reference
		for _, raw := range episodeEntries(values) {
			item := object(raw)
			if text(item, "kind") == "reported_experience" {
				ref := object(value(item, "assertionRef"))
				refs = append(refs, Reference{ID: text(ref, "id"), Version: int64(episodeNumber(ref, "version")), DeletionEpoch: int64(episodeNumber(ref, "deletionEpoch"))})
			}
		}
		return tracker.Capture(ctx, c, refs)
	})
}

func episodeEntries(values []*Object) []any {
	page := object(value(values[0], "page"))
	return epArr(value(page, "items"))
}

func (p episodeExposurePort) Query(ctx context.Context, c Context, input any) ([]*Object, error) {
	var enabled, answerPlans bool
	if err := p.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		return p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			settings, err := (&commitmentExposurePort{p.memory}).settings(scope)
			enabled = truthy(value(settings, "memoryEpisodesEnabled"))
			answerPlans = truthy(value(settings, "memoryCommitmentsEnabled")) && truthy(value(settings, "memoryCommitmentsAnswerEnabled"))
			return err
		})
	}); err != nil || !enabled {
		return []*Object{}, err
	}
	page, err := p.memory.memory.prepareEpisodes(ctx, EpisodesQuery{SceneID: c.SceneID, PersonID: input.(string)}, answerPlans)
	if err != nil {
		return nil, err
	}
	return []*Object{epObj("stateKind", "experience_view", "page", page)}, nil
}

func (p episodeExposurePort) Valid(ctx context.Context, c Context, values []*Object) (bool, error) {
	valid := false
	err := p.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { valid = p.valid(reads, c, values); return nil })
	return valid && err == nil, nil
}

func (p episodeExposurePort) ValidScoped(reads botruntime.ReadAuthority, c Context, values []*Object) (bool, error) {
	return p.valid(reads, c, values), nil
}

func (p episodeExposurePort) valid(reads botruntime.ReadAuthority, c Context, values []*Object) bool {
	if len(values) != 1 || text(values[0], "stateKind") != "experience_view" {
		return false
	}
	valid := false
	err := p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		settings, err := (&commitmentExposurePort{p.memory}).settings(scope)
		if err != nil || !truthy(value(settings, "memoryEpisodesEnabled")) {
			return err
		}
		page := object(value(values[0], "page"))
		coverage := text(object(value(page, "coverage")), "status")
		if coverage != "complete" && coverage != "limited" {
			return nil
		}
		valid = experienceview.Verify(c.SceneID, page, func(scene string, refs *Object) (fresh *Object, err error) {
			d := p.memory.memory.d
			err = d.Commitments.ReadRuntimeCurrent(scope, d.Assertions, func(commitments.Context) (bool, error) {
				return truthy(value(settings, "memoryCommitmentsEnabled")), nil
			}, d.Authority, func(read *commitments.ReadService) error {
				resolver, err := memoryworkspace.NewResolver(read, d.Authority.DateParser)
				if err != nil {
					return err
				}
				fresh, err = resolveEpisodeEntries(commitments.Context{SceneID: scene}, read, resolver, refs)
				return err
			})
			return
		})
		return nil
	})
	// TS WorkspaceExposure validity catches verification/read failures.
	return valid && err == nil
}

func (p episodeExposurePort) SourceIDs(values []*Object) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, raw := range episodeEntries(values) {
		item := object(raw)
		switch text(item, "kind") {
		case "reported_experience":
			for _, raw := range epArr(value(object(value(item, "record")), "sources")) {
				add(text(object(raw), "messageId"))
			}
		case "shared_plan_declaration":
			for _, id := range commitmentViewSources(object(value(item, "view"))) {
				add(id)
			}
		}
	}
	return ids, nil
}
