package memoryservice

import (
	"errors"
	"math"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryread"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

type semanticModelPort struct {
	memory *ExposurePort
	policy *modelread.RuntimePolicy
	now    func() int64
}

func (p *semanticModelPort) allowed(reads botruntime.ReadAuthority, c Context) (allowed bool, err error) {
	if c.Simulation {
		return false, nil
	}
	allowed, err = p.policy.AllowedScoped(reads, c.SceneID)
	if err != nil || !allowed {
		return
	}
	err = p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		state, e := semanticReadState(func(q string, args ...any) (semanticRows, error) { return scope.Query(q, args...) }, c.SceneID)
		if e != nil {
			return e
		}
		allowed = state.settings["memorySemanticEnabled"] == true && state.settings["memorySemanticAnswerEnabled"] == true
		return nil
	})
	return
}
func (p *semanticModelPort) revision(reads botruntime.ReadAuthority) (out uint64, err error) {
	err = p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		s, e := semanticReadState(func(q string, args ...any) (semanticRows, error) { return scope.Query(q, args...) }, "")
		out = s.revision
		return e
	})
	return
}

// load reuses current semantic assertion and episode owners. It does not replace
// a stale ref, fabricate provenance, silently drop unavailable partition members,
// or acquire a second database connection from within the caller's scope.
func (p *semanticModelPort) load(reads botruntime.ReadAuthority, c Context) (page *Object, err error) {
	err = p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		if e := scope.CheckBinding(p.memory.main, p.memory.authority); e != nil {
			return e
		}
		state, e := semanticReadState(func(q string, args ...any) (semanticRows, error) { return scope.Query(q, args...) }, c.SceneID)
		if e != nil {
			return e
		}
		d := p.memory.memory.d
		opts := d.Authority
		opts.NowMillis = p.now
		return d.Commitments.ReadRuntimeCurrent(scope, d.Assertions, func(commitments.Context) (bool, error) {
			return state.settings["memoryCommitmentsEnabled"] == true, nil
		}, opts, func(current *commitments.ReadService) error {
			authority := semantic.PrivacyAuthority{AssertReady: current.AssertReady, Current: func(c semantic.Context, ref *Object) (*Object, error) {
				return semanticCurrent(current, Context{SceneID: c.SceneID, Simulation: c.Simulation}, ref)
			}, AnchorCurrent: func(c semantic.Context, ref *Object) (*Object, error) {
				v, e := current.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, semanticRef(ref))
				if e != nil || v == nil {
					return nil, e
				}
				return epObj("record", v.Record, "proofs", v.Proofs), nil
			}}
			authority.PartitionKey = func(c semantic.Context, person any) (out string, err error) {
				err = p.memory.withScopeReader(scope, func(r *assertions.Reader) error {
					read, e := memoryread.New(r, opts)
					if e != nil {
						return e
					}
					records, e := read.List(memoryread.Input{SceneID: c.SceneID, Simulation: c.Simulation})
					if e != nil {
						return e
					}
					selected := []*Object{}
					for _, record := range records {
						sc := object(value(record, "scope"))
						if !truthy(value(record, "internalUse")) && text(record, "origin") == "learned" && text(sc, "kind") == "scene" && text(sc, "sceneId") == c.SceneID && deepEqual(value(sc, "personId"), person) {
							selected = append(selected, record)
						}
					}
					if len(selected) < 2 || len(selected) > 12 {
						return nil
					}
					selections := []any{}
					for _, record := range selected {
						st, e := r.State(text(record, "id"))
						if e != nil {
							return e
						}
						if st.Version < 1 || st.Deleted {
							return nil
						}
						ref := epObj("id", text(record, "id"), "version", float64(st.Version), "deletionEpoch", float64(st.DeletionEpoch))
						live, e := authority.Current(c, ref)
						if e != nil {
							return e
						}
						if live == nil {
							return nil
						}
						selections = append(selections, epObj("ref", ref, "dependencyKey", value(live, "dependencyKey")))
					}
					sort.SliceStable(selections, func(i, j int) bool {
						return epLess(text(object(epGet(selections[i], "ref")), "id"), text(object(epGet(selections[j], "ref")), "id"))
					})
					out, e = semanticHash(selections)
					return e
				})
				return
			}
			authority.EpisodeAnchor = func(c semantic.Context, anchor *Object, parents []any, key string) (valid bool, err error) {
				err = d.Knowledge.Storage.ReadRuntimeCurrent(scope, d.Assertions, d.Commitments, knowledge.ReadOptions{KnowledgeEnabled: state.settings["memoryKnowledgeEnabled"] == true, CommitmentEnabled: state.settings["memoryCommitmentsEnabled"] == true, Authority: opts}, func(reader *knowledge.Reader) error {
					ports := episodePorts{query: reader.Query, current: func(c Context, r *Object) (*Object, error) { return semanticCurrent(current, c, r) }, anchor: func(c Context, r *Object) (*commitments.Assertion, error) {
						return current.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, semanticRef(r))
					}}
					var e error
					valid, e = ports.valid(Context{SceneID: c.SceneID, Simulation: c.Simulation}, anchor, parents, &key)
					return e
				})
				return
			}
			return d.Semantic.ReadRuntimeCurrent(scope, semantic.ModelReadPolicy{Authority: authority, Enabled: func(semantic.Context) (bool, error) { return state.settings["memorySemanticEnabled"] == true, nil }, NowMillis: func() float64 { return float64(p.now()) }, ParseDate: func(s string) (float64, error) {
				ms, valid, e := opts.DateParser.ParseMillis(s)
				if !valid {
					return math.NaN(), e
				}
				return float64(ms), e
			}}, func(reader *semantic.ModelReader) error {
				var e error
				page, e = reader.List(semantic.Context{SceneID: c.SceneID, Simulation: c.Simulation}, 12)
				return e
			})
		})
	})
	if err != nil {
		return nil, err
	}
	personal, err := p.policy.PersonalizationAllowedScoped(reads, c.SceneID)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, raw := range epArr(value(page, "items")) {
		view := object(raw)
		candidate := object(value(view, "candidate"))
		if text(candidate, "kind") == "summary_candidate" && text(candidate, "status") != "admitted" {
			continue
		}
		items = append(items, view)
	}
	if !personal {
		for _, raw := range items {
			for _, d := range epArr(epGet(raw, "declarations")) {
				record := object(epGet(d, "record"))
				sc := object(value(record, "scope"))
				kind := text(record, "kind")
				if text(sc, "kind") != "scene" || truthy(value(sc, "personId")) || (kind != "fact" && kind != "claim" && kind != "term") {
					return nil, errors.New("personalization_disabled")
				}
			}
		}
	}
	_ = page.Set("items", items)
	return page, nil
}
