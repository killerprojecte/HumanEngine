package memoryservice

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/experienceview"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryworkspace"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

type EpisodesQuery struct{ SceneID, PersonID string }
type EpisodesAdminRead func(context.Context, *EpisodesQuery, func(int, []byte) error) (bool, error)

type episodesFlags struct{ real, enabled, commitments bool }

func loadEpisodesFlags(r *assertions.Reader, schemas *contract.Schemas, scene string) (flags episodesFlags, err error) {
	flags.real, err = realPeopleScene(r, scene)
	if err != nil || !flags.real {
		return
	}
	raw, exists, err := r.SettingsInput()
	if err != nil {
		return flags, err
	}
	input := contract.DefaultSettingsInput()
	if exists {
		input = []byte(raw)
	}
	settings, err := schemas.Parse("Settings", input)
	if err != nil {
		return flags, err
	}
	flags.enabled = settings["memoryEpisodesEnabled"] == true
	flags.commitments = settings["memoryCommitmentsEnabled"] == true && settings["memoryCommitmentsAnswerEnabled"] == true
	return flags, nil
}

// BindEpisodesAdminRead composes the source GET's legacy capture and synchronous
// metadata refresh before a fresh, source-qualified publication. No model or
// independent writer is introduced.
func (s *Service) BindEpisodesAdminRead(publisher *recovery.ReadPublisher, schemas *contract.Schemas) (EpisodesAdminRead, error) {
	publish, err := s.d.Commitments.BindPublishedCurrent(publisher, s.d.Assertions, s.d.Authority)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, q *EpisodesQuery, emit func(int, []byte) error) (bool, error) {
		var initial episodesFlags
		var page *Object
		if q != nil {
			if err := s.d.Assertions.Read(ctx, func(r *assertions.Reader) (err error) {
				initial, err = loadEpisodesFlags(r, schemas, q.SceneID)
				return err
			}); err != nil {
				return false, err
			}
			if initial.real && initial.enabled {
				var err error
				page, err = s.prepareAdminEpisodes(ctx, *q, initial.commitments)
				if err != nil {
					return true, err
				}
			}
		}
		started, allowPlans := false, false
		status := http.StatusOK
		err := publish(ctx, func(*assertions.Reader, commitments.Context) (bool, error) {
			return allowPlans, nil
		}, func(r *assertions.Reader, read *commitments.ReadService, _ commitments.PublicationQuery) ([]byte, error) {
			started = true
			if q == nil {
				status = http.StatusBadRequest
				return []byte(`{"error":"Invalid request"}`), nil
			}
			flags, err := loadEpisodesFlags(r, schemas, q.SceneID)
			if err != nil {
				return nil, err
			}
			if !flags.real {
				status = http.StatusNotFound
				return []byte(`{"error":"Scene unavailable"}`), nil
			}
			if !flags.enabled {
				return projectAdminEpisodes(disabledEpisodes(), false)
			}
			allowPlans = flags.commitments
			if page == nil || initial.commitments != flags.commitments {
				return nil, errors.New("memory_episodes_changed")
			}
			resolver, err := memoryworkspace.NewResolver(read, s.d.Authority.DateParser)
			if err != nil {
				return nil, err
			}
			fresh := experienceview.Verify(q.SceneID, page, func(scene string, refs *Object) (*Object, error) {
				return resolveEpisodeEntries(commitments.Context{SceneID: scene}, read, resolver, refs)
			})
			if !fresh {
				return nil, errors.New("memory_episodes_changed")
			}
			return projectAdminEpisodes(page, true)
		}, func(body []byte) error { return emit(status, body) })
		return started, err
	}, nil
}

func disabledEpisodes() *Object {
	return epObj("items", []any{}, "coverage", epObj("status", "complete", "returned", float64(0), "omittedEntries", float64(0)),
		"interpretation", "independent_qualified_records_not_synthesized_episodes", "sourceBodyCoverage", "unavailable")
}

func (s *Service) prepareAdminEpisodes(ctx context.Context, q EpisodesQuery, allowPlans bool) (*Object, error) {
	scene := Context{SceneID: q.SceneID}
	candidates, err := s.List(ctx, scene, "")
	if err != nil {
		return nil, err
	}
	candidates = episodeCandidates(candidates, q)
	var compareErr error
	sort.SliceStable(candidates, func(i, j int) bool {
		if compareErr != nil {
			return false
		}
		a, b := candidates[i], candidates[j]
		order, err := s.d.Authority.Compare(text(b, "learnedAt"), text(a, "learnedAt"))
		if err == nil && order == 0 {
			order, err = s.d.Authority.Compare(text(a, "id"), text(b, "id"))
		}
		compareErr = err
		return order < 0
	})
	if compareErr != nil {
		return nil, compareErr
	}
	limited := len(candidates) > 28
	candidates = candidates[:min(len(candidates), 28)]
	refs, err := s.CaptureParentReferences(ctx, scene, candidates)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*Object, len(candidates))
	for _, record := range candidates {
		byID[text(record, "id")] = record
	}
	var page *Object
	var selections []workingset.Selection
	c := commitments.Context{SceneID: q.SceneID}
	err = s.d.Commitments.ReadCurrent(ctx, s.d.Assertions, s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
		resolver, err := memoryworkspace.NewResolver(read, s.d.Authority.DateParser)
		if err != nil {
			return err
		}
		assertionItems := make([]any, 0, len(refs))
		workingRefs := make([]workingset.Reference, 0, len(refs))
		for _, ref := range refs {
			wr := workingset.Reference{Kind: "assertion", ID: ref.ID, Version: float64(ref.Version), DeletionEpoch: float64(ref.DeletionEpoch)}
			snapshot, err := resolver.AssertionSnapshot(c, wr)
			if err != nil {
				return err
			}
			if snapshot != nil && deepEqual(value(snapshot, "record"), byID[ref.ID]) {
				assertionItems = append(assertionItems, snapshot)
				workingRefs = append(workingRefs, wr)
			}
		}
		asCoverage, planCoverage := "complete", "complete"
		if limited || len(assertionItems) != len(candidates) {
			asCoverage = "limited"
		}
		planItems := []any{}
		if allowPlans {
			plans, err := read.List(c, commitments.ReadOptions{PersonID: q.PersonID})
			if err != nil {
				return err
			}
			planCoverage = text(plans, "coverage")
			planItems = epArr(value(plans, "items"))
			for _, plan := range planItems {
				v := object(plan)
				ref := object(value(v, "ref"))
				mutation, _ := value(v, "mutationVersion").(float64)
				workingRefs = append(workingRefs, workingset.Reference{Kind: "commitment", ID: text(ref, "id"), Version: episodeNumber(ref, "version"), DeletionEpoch: episodeNumber(ref, "deletionEpoch"), MutationVersion: &mutation})
			}
		}
		snapshots, err := resolver.Resolve(workingset.Context{SceneID: q.SceneID}, workingRefs)
		if err != nil {
			return err
		}
		selections = make([]workingset.Selection, 0, len(snapshots))
		for _, snapshot := range snapshots {
			selections = append(selections, snapshot.Selection())
		}
		page, err = experienceview.Project(epObj("sceneId", q.SceneID, "simulation", false, "assertions", assertionItems,
			"commitments", planItems, "upstreamCoverage", epObj("assertions", asCoverage, "commitments", planCoverage)))
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(selections) != 0 {
		scope := workingset.Context{SceneID: q.SceneID}
		if _, err := s.d.WorkingSet.Enqueue(ctx, scope, selections); err != nil {
			return nil, err
		}
		if _, err := s.d.WorkingSet.Drain(ctx, scope, 40); err != nil {
			return nil, err
		}
	}
	return page, nil
}

func episodeCandidates(records []*Object, q EpisodesQuery) []*Object {
	out := records[:0]
	for _, record := range records {
		scope := object(value(record, "scope"))
		if text(record, "kind") == "experience" && !truthy(value(record, "internalUse")) &&
			text(scope, "kind") == "scene" && text(scope, "sceneId") == q.SceneID &&
			(q.PersonID == "" || text(scope, "personId") == q.PersonID) {
			out = append(out, record)
		}
	}
	return out
}

func episodeNumber(o *Object, name string) float64 { n, _ := value(o, name).(float64); return n }

func resolveEpisodeEntries(c commitments.Context, read *commitments.ReadService, resolver *memoryworkspace.Resolver, refs *Object) (*Object, error) {
	assertions, plans := []any{}, []any{}
	for _, raw := range epArr(value(refs, "assertions")) {
		ref := object(raw)
		current, err := resolver.AssertionSnapshot(c, workingset.Reference{Kind: "assertion", ID: text(ref, "id"), Version: episodeNumber(ref, "version"), DeletionEpoch: episodeNumber(ref, "deletionEpoch")})
		if err != nil {
			return nil, err
		}
		if current != nil {
			assertions = append(assertions, current)
		}
	}
	for _, raw := range epArr(value(refs, "commitments")) {
		ref := object(raw)
		current, err := read.Current(c, commitments.Reference{ID: text(ref, "id"), Version: episodeNumber(ref, "version"), DeletionEpoch: episodeNumber(ref, "deletionEpoch")})
		if err != nil {
			return nil, err
		}
		if current != nil && value(current, "mutationVersion") == value(ref, "mutationVersion") {
			plans = append(plans, current)
		}
	}
	return epObj("assertions", assertions, "commitments", plans), nil
}

func projectAdminEpisodes(page *Object, enabled bool) ([]byte, error) {
	items := epArr(value(page, "items"))
	out := make([]any, 0, len(items))
	for _, raw := range items {
		entry := object(raw)
		if text(entry, "kind") == "reported_experience" {
			r := object(value(entry, "record"))
			if !validEpisodeID(value(r, "id")) {
				return nil, experienceview.ErrInvalid
			}
			record := episodeFields(r, "id", "content", "kind", "status", "learnedAt")
			for _, key := range []string{"epistemicKind", "occurredAt", "validFrom", "validUntil"} {
				if v, present := r.Get(key); present {
					if _, ok := v.(string); !ok {
						return nil, experienceview.ErrInvalid
					}
					_ = record.Set(key, v)
				}
			}
			sources := []any{}
			for _, rawSource := range epArr(value(r, "sources")) {
				source := object(rawSource)
				scene, message, sender := value(source, "sceneId"), value(source, "messageId"), value(source, "senderId")
				if !validEpisodeID(scene) || !validEpisodeID(message) || !validEpisodeID(sender) {
					return nil, experienceview.ErrInvalid
				}
				sources = append(sources, epObj("sceneId", scene, "messageId", message, "senderId", sender))
			}
			_ = record.Set("sources", sources)
			out = append(out, epObj("kind", "reported_experience", "record", record, "time", value(entry, "time")))
			continue
		}
		p := object(value(entry, "view"))
		view := episodeFields(p, "terms", "recordedAt", "confirmation", "lifecycle", "qualification", "sourceBodyCoverage",
			"confirmedParticipantIds", "missingParticipantIds", "completedParticipantIds", "cancelledParticipantIds")
		_ = view.Set("sourceMessageIds", episodePlanSources(p))
		out = append(out, epObj("kind", "shared_plan_declaration", "view", view, "completionBasis", value(entry, "completionBasis")))
	}
	return jsjson.Marshal(epObj("enabled", enabled, "interpretation", value(page, "interpretation"),
		"sourceBodyCoverage", value(page, "sourceBodyCoverage"),
		"coverage", episodeFields(object(value(page, "coverage")), "status", "returned", "omittedEntries"), "items", out))
}

func validEpisodeID(v any) bool {
	id, ok := v.(string)
	if !ok {
		return false
	}
	n, err := jsjson.CodePointCount(id)
	return err == nil && n >= 1 && n <= 512
}

func episodeFields(source *Object, keys ...string) *Object {
	out := new(Object)
	for _, key := range keys {
		if v, found := source.Get(key); found {
			_ = out.Set(key, v)
		}
	}
	return out
}

func episodePlanSources(plan *Object) []any {
	seen := map[string]bool{}
	ids := []any{}
	add := func(evidence []any) {
		for _, item := range evidence {
			for _, proof := range epArr(epGet(item, "proofs")) {
				for _, source := range epArr(epGet(proof, "sources")) {
					id := text(object(source), "messageId")
					if !seen[id] {
						seen[id] = true
						ids = append(ids, id)
					}
				}
			}
		}
	}
	add(epArr(value(plan, "evidence")))
	for _, group := range epArr(value(plan, "requiredProposalGroups")) {
		add(epArr(group))
	}
	return ids
}
