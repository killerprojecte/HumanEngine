package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryread"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"sort"
)

type semanticNeighborhood struct {
	current   func(Context, *Object) (*Object, error)
	records   func(Context) ([]*Object, error)
	refs      func(Context, []*Object) ([]*Object, error)
	partition func(Context, any) (string, error)
	episodes  func(Context, *Object) (*Object, error)
}

func (s *Service) semanticNeighborhood(ctx context.Context, tx *assertions.RevisionTxn, ws *recovery.WriteSession) semanticNeighborhood {
	authority := s.semanticPrivacyAuthority(ctx, tx, ws)
	return semanticNeighborhood{
		current: func(c Context, r *Object) (*Object, error) {
			return authority.Current(semantic.Context{SceneID: c.SceneID, Simulation: c.Simulation}, r)
		},
		records: func(c Context) ([]*Object, error) {
			if e := ready(ctx, tx.Store); e != nil {
				return nil, e
			}
			if !c.Simulation {
				if e := tx.Transaction(ctx, s.refresh); e != nil {
					return nil, e
				}
			}
			return s.read(ctx, tx.Store, memoryread.Input{SceneID: c.SceneID, Simulation: c.Simulation})
		},
		refs: func(c Context, records []*Object) ([]*Object, error) {
			return s.capturePrivacyParents(ctx, tx, semantic.Context{SceneID: c.SceneID, Simulation: c.Simulation}, records)
		},
		partition: func(c Context, person any) (string, error) {
			return authority.PartitionKey(semantic.Context{SceneID: c.SceneID, Simulation: c.Simulation}, person)
		},
		episodes: func(c Context, ref *Object) (out *Object, err error) {
			err = s.semanticEpisodesScoped(ctx, tx, ws, func(p episodePorts) error { var e error; out, e = p.discover(c, ref); return e })
			return
		},
	}
}
func (s *Service) semanticEpisodesScoped(ctx context.Context, tx *assertions.RevisionTxn, ws *recovery.WriteSession, fn func(episodePorts) error) error {
	state, e := semanticSessionState(ws, "")
	if e != nil {
		return e
	}
	return s.d.WorkingSet.WithSession(ws).ReadScope(ctx, func(scope *workingset.Scope) error {
		return s.d.Knowledge.WithSession(ws).Storage.ReadWorkingSetEpisode(ctx, scope, tx.Store, s.d.Commitments.WithSession(ws), knowledge.ReadOptions{KnowledgeEnabled: state.settings["memoryKnowledgeEnabled"] == true, Authority: s.d.Authority}, func(reader *knowledge.Reader, read *commitments.ReadService) error {
			return fn(episodePorts{query: reader.Query, current: func(c Context, r *Object) (*Object, error) { return semanticCurrent(read, c, r) }, anchor: func(c Context, r *Object) (*commitments.Assertion, error) {
				return read.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, semanticRef(r))
			}})
		})
	})
}
func (n semanticNeighborhood) episode(c Context, trigger, anchor *Object) (*semantic.ResolvedInput, error) {
	page, e := n.episodes(c, trigger)
	if e != nil {
		return nil, e
	}
	for _, v := range epArr(value(page, "groups")) {
		group := object(v)
		a := object(value(group, "anchor"))
		if !epEqual(value(a, "id"), value(anchor, "id")) || !epEqual(value(a, "version"), value(anchor, "version")) {
			continue
		}
		entries := []semantic.InputEntry{}
		parents := epArr(value(group, "parents"))
		for _, selection := range parents {
			assertion, e := n.current(c, object(epGet(selection, "ref")))
			if e != nil {
				return nil, e
			}
			if assertion != nil && epEqual(value(assertion, "dependencyKey"), epGet(selection, "dependencyKey")) {
				entries = append(entries, semantic.InputEntry{Selection: object(selection), Assertion: assertion})
			}
		}
		if len(entries) != len(parents) {
			return nil, nil
		}
		membership := text(group, "anchorMembershipKey")
		return &semantic.ResolvedInput{Coverage: "complete", Entries: entries, EpisodeAnchor: a, EpisodeMembershipKey: &membership, AnchorDependencies: epArr(value(group, "anchorDependencies")), AnchorQualifications: epArr(value(group, "anchorQualifications"))}, nil
	}
	return nil, nil
}
func (n semanticNeighborhood) resolve(dirty []semantic.Dirty, ticket *modelread.Ticket) (*semantic.ResolvedInput, error) {
	if len(dirty) == 0 || dirty[0].SceneID == "" || ticket == nil || ticket.SceneID() != dirty[0].SceneID {
		return nil, nil
	}
	scene := dirty[0].SceneID
	for _, d := range dirty {
		if d.SceneID != scene {
			return nil, nil
		}
	}
	c := Context{SceneID: scene}
	var first *semantic.Dirty
	for i := range dirty {
		item := &dirty[i]
		v, e := n.current(c, object(value(item.Trigger, "ref")))
		if e != nil {
			return nil, e
		}
		if v != nil && epEqual(value(v, "dependencyKey"), value(item.Trigger, "dependencyKey")) {
			first = item
			break
		}
	}
	if first == nil {
		return nil, nil
	}
	if first.EpisodeAnchor != nil {
		if !ticket.PersonalizationEnabled() {
			return nil, nil
		}
		return n.episode(c, object(value(first.Trigger, "ref")), first.EpisodeAnchor)
	}
	trigger, e := n.current(c, object(value(first.Trigger, "ref")))
	if e != nil || trigger == nil {
		return nil, e
	}
	scope := object(epGet(value(trigger, "record"), "scope"))
	if text(scope, "kind") != "scene" {
		return nil, nil
	}
	person := value(scope, "personId")
	if truthy(person) && !ticket.PersonalizationEnabled() {
		return nil, nil
	}
	all, e := n.records(c)
	if e != nil {
		return nil, e
	}
	records := []*Object{}
	for _, r := range all {
		scope := object(value(r, "scope"))
		kind := text(r, "kind")
		if !truthy(value(r, "internalUse")) && text(r, "origin") == "learned" && text(scope, "kind") == "scene" && text(scope, "sceneId") == scene && epEqual(value(scope, "personId"), person) && (ticket.PersonalizationEnabled() || kind == "fact" || kind == "claim" || kind == "term") {
			records = append(records, r)
		}
	}
	if len(records) > 12 {
		return &semantic.ResolvedInput{Coverage: "limited", Entries: []semantic.InputEntry{}}, nil
	}
	refs, e := n.refs(c, records)
	if e != nil {
		return nil, e
	}
	if len(refs) != len(records) {
		return nil, nil
	}
	entries := []semantic.InputEntry{}
	for _, ref := range refs {
		a, e := n.current(c, ref)
		if e != nil {
			return nil, e
		}
		if a != nil {
			entries = append(entries, semantic.InputEntry{Selection: epObj("ref", ref, "dependencyKey", value(a, "dependencyKey")), Assertion: a})
		}
	}
	coverage := "complete"
	if len(entries) != len(records) {
		coverage = "limited"
	}
	partition, e := n.partition(c, person)
	if e != nil {
		return nil, e
	}
	var key *string
	if partition != "" {
		key = &partition
	}
	return &semantic.ResolvedInput{Coverage: coverage, Entries: entries, PartitionKey: key}, nil
}
func (n semanticNeighborhood) valid(snapshot *semantic.Input) (bool, error) {
	if snapshot == nil {
		return false, nil
	}
	c := Context{SceneID: snapshot.SceneID()}
	selections := snapshot.Selections()
	if len(selections) == 0 {
		return false, nil
	}
	anchor := object(value(snapshot.Value, "episodeAnchor"))
	if anchor != nil {
		current, e := n.episode(c, object(epGet(selections[0], "ref")), anchor)
		if e != nil || current == nil {
			return false, e
		}
		return semantic.InputUnchanged(snapshot, *current), nil
	}
	entries := []semantic.InputEntry{}
	for _, selection := range selections {
		ref := object(epGet(selection, "ref"))
		a, e := n.current(c, ref)
		if e != nil {
			return false, e
		}
		if a != nil {
			entries = append(entries, semantic.InputEntry{Selection: epObj("ref", ref, "dependencyKey", value(a, "dependencyKey")), Assertion: a})
		}
	}
	if value(snapshot.Value, "episodeOnly") != true {
		ordered := append([]any(nil), selections...)
		sort.SliceStable(ordered, func(i, j int) bool {
			return epLess(text(object(epGet(ordered[i], "ref")), "id"), text(object(epGet(ordered[j], "ref")), "id"))
		})
		expected, e := semanticHash(ordered)
		if e != nil {
			return false, e
		}
		key, e := n.partition(c, value(snapshot.Value, "personId"))
		if e != nil {
			return false, e
		}
		if key != expected {
			return false, nil
		}
	}
	key, e := n.partition(c, value(snapshot.Value, "personId"))
	if e != nil {
		return false, e
	}
	var partition *string
	if key != "" {
		partition = &key
	}
	return semantic.InputUnchanged(snapshot, semantic.ResolvedInput{Coverage: "complete", Entries: entries, PartitionKey: partition}), nil
}
func (s *Service) ResolveSemantic(ctx context.Context, dirty []semantic.Dirty, ticket *modelread.Ticket) (out *semantic.ResolvedInput, err error) {
	err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
			var e error
			out, e = s.semanticNeighborhood(ctx, tx, ws).resolve(dirty, ticket)
			return e
		})
	})
	return
}
func (s *Service) ValidSemantic(ctx context.Context, snapshot *semantic.Input) (out bool, err error) {
	err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
			var e error
			out, e = s.semanticNeighborhood(ctx, tx, ws).valid(snapshot)
			return e
		})
	})
	return
}
func (s *Service) BeginSemanticAttempt(ctx context.Context, snapshot *semantic.Input) (out bool, err error) {
	err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
			valid, e := s.semanticNeighborhood(ctx, tx, ws).valid(snapshot)
			if e != nil || !valid {
				return e
			}
			state, e := semanticSessionState(ws, snapshot.SceneID())
			if e != nil {
				return e
			}
			out, e = s.d.Semantic.WithSession(ws).BeginAttempt(semantic.Context{SceneID: snapshot.SceneID()}, snapshot.Key(), snapshot.Selections(), s.semanticPrivacyAuthority(ctx, tx, ws), state.admission())
			return e
		})
	})
	return
}
func (s *Service) FinishSemanticAttempt(ctx context.Context, snapshot *semantic.Input, status string) error {
	return s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
			return s.d.Semantic.WithSession(ws).FinishAttempt(semantic.Context{SceneID: snapshot.SceneID()}, snapshot.Key(), status)
		})
	})
}
func (s *Service) commitSemantic(ctx context.Context, snapshot *semantic.Input, proposals []*Object, guard func(*assertions.RevisionTxn, *recovery.WriteSession) error) (out semantic.CommitResult, err error) {
	err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
			if e := guard(tx, ws); e != nil {
				return e
			}
			valid, e := s.semanticNeighborhood(ctx, tx, ws).valid(snapshot)
			if e != nil {
				return e
			}
			if !valid {
				return errors.New("semantic_authority_changed")
			}
			state, e := semanticSessionState(ws, snapshot.SceneID())
			if e != nil {
				return e
			}
			inputs := []any{}
			for _, p := range proposals {
				inputs = append(inputs, p)
			}
			var trusted *semantic.TrustedEpisode
			if anchor := object(value(snapshot.Value, "episodeAnchor")); anchor != nil && truthy(value(snapshot.Value, "episodeMembershipKey")) {
				trusted = &semantic.TrustedEpisode{Anchor: anchor, MembershipKey: text(snapshot.Value, "episodeMembershipKey"), Dependencies: epArr(value(snapshot.Value, "anchorDependencies")), Qualifications: epArr(value(snapshot.Value, "anchorQualifications"))}
			}
			result, e := s.d.Semantic.WithSession(ws).Admit(semantic.Context{SceneID: snapshot.SceneID()}, snapshot.Selections(), inputs, trusted, s.semanticPrivacyAuthority(ctx, tx, ws), state.admission())
			out = semantic.CommitResult{Created: len(result.Created), Rejected: len(result.Rejected)}
			return e
		})
	})
	return
}
