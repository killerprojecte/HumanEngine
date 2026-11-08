package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorylexical"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"

	"sort"
	"strings"
)

type LearningTargets struct {
	Targets  []*Object
	Coverage string
	Limits   *Object
}

// ReconciliationTargets preserves source metadata selection before semantic
// hits. Candidate metadata narrows relevance and never grants read authority.
func (s *Service) ReconciliationTargets(ctx context.Context, input *Object, candidates, semantic []*Object) (out LearningTargets, err error) {
	out = LearningTargets{Targets: []*Object{}, Coverage: "complete", Limits: epObj("perCandidate", float64(4), "perGroup", float64(8), "total", float64(16), "semantic", float64(8))}
	scene := text(input, "sceneId")
	enabled := false
	err = s.d.Assertions.Read(ctx, func(r *assertions.Reader) error {
		v, e := r.Scene(scene)
		enabled = value(v, "learningEnabled") == true
		return e
	})
	if err != nil || value(input, "simulation") == true || !enabled {
		return
	}
	if err = s.RefreshSupport(ctx); err != nil {
		return
	}
	capped := len(candidates) > 8
	type group struct {
		person     *string
		kind       string
		candidates []*Object
	}
	groups := []*group{}
	keys := map[string]*group{}
	selected := map[string]*Object{}
	err = s.d.Assertions.Read(ctx, func(r *assertions.Reader) error {
		a, e := assertionauthority.New(r, s.d.Authority)
		if e != nil {
			return e
		}
		kindGroup := func(k string) string {
			if k == "fact" || k == "claim" {
				return "statement"
			}
			return k
		}
		for _, candidate := range candidates[:min(8, len(candidates))] {
			sources := []any{}
			ids := epArr(value(candidate, "sourceMessageIds"))
			valid := len(ids) > 0
			for _, id := range ids {
				var found *Object
				for _, v := range epArr(value(input, "sourceEvidence")) {
					ev := object(v)
					if value(ev, "messageId") == id && text(ev, "sceneId") == scene && text(ev, "origin") == "live" && value(ev, "available") == true && value(ev, "isBot") != true {
						recalled, e := r.Recalled(scene, text(ev, "messageId"))
						if e != nil {
							return e
						}
						if !recalled {
							found = ev
							break
						}
					}
				}
				if found == nil {
					valid = false
					break
				}
				sources = append(sources, found)
			}
			if !valid {
				continue
			}
			c := candidate.Clone()
			_ = c.Set("sources", sources)
			at := sourceadmission.ResolveSubject(input, c)
			if value(at, "ok") != true {
				continue
			}
			for _, key := range []string{"personId", "kind", "epistemicKind"} {
				if v, ok := at.Get(key); ok {
					_ = c.Set(key, v)
				} else {
					c.Delete(key)
				}
			}
			key, e := jsjson.Stringify([]any{value(c, "personId"), kindGroup(text(c, "kind"))})
			if e != nil {
				return e
			}
			g := keys[key]
			if g == nil {
				g = &group{kind: text(c, "kind")}
				if p, ok := value(c, "personId").(string); ok {
					g.person = &p
				}
				keys[key] = g
				groups = append(groups, g)
			}
			g.candidates = append(g.candidates, c)
		}
		if len(groups) > 0 && len(semantic) > 8 {
			capped = true
		}
		eligible := func(record *Object) (bool, error) {
			ok, e := a.Supported(record)
			if e != nil || !ok {
				return false, e
			}
			return a.CurrentApplicable(record)
		}
		for _, g := range groups {
			kinds := []string{g.kind}
			if kindGroup(g.kind) == "statement" {
				kinds = []string{"fact", "claim"}
			}
			rows, e := r.LearningTargetCandidates(scene, g.person, kinds)
			if e != nil {
				return e
			}
			if len(rows) > 8 {
				capped = true
			}
			metadata := []*Object{}
			for _, record := range rows[:min(8, len(rows))] {
				ok, e := eligible(record)
				if e != nil {
					return e
				}
				if ok {
					metadata = append(metadata, record)
				}
			}
			hits := []*Object{}
			for _, hit := range semantic[:min(8, len(semantic))] {
				current, e := r.Record(text(hit, "id"))
				if e != nil {
					return e
				}
				if current == nil || truthy(value(current, "internalUse")) {
					continue
				}
				ok, e := eligible(current)
				if e != nil {
					return e
				}
				if !ok {
					continue
				}
				kindOK := false
				for _, k := range kinds {
					if text(current, "kind") == k {
						kindOK = true
					}
				}
				if !kindOK {
					continue
				}
				scope := object(value(current, "scope"))
				if text(scope, "kind") == "scene" {
					if text(scope, "sceneId") != scene {
						continue
					}
					p, has := value(scope, "personId").(string)
					if (g.person == nil && has) || (g.person != nil && (!has || p != *g.person)) {
						continue
					}
				} else if g.person != nil {
					continue
				}
				hits = append(hits, current)
			}
			groupIDs := map[string]bool{}
			for _, c := range g.candidates {
				tokens := []string{}
				seen := map[string]bool{}
				lower, e := jsstring.Lower(text(c, "content"))
				if e != nil {
					return e
				}
				runs, e := memorylexical.TokenRuns(lower)
				if e != nil {
					return e
				}
				for _, t := range runs {
					length, e := jsjson.UTF16Len(t)
					if e != nil {
						return e
					}
					if length > 1 && !seen[t] {
						tokens = append(tokens, t)
						seen[t] = true
					}
				}
				score := func(record *Object) int {
					n := 0
					content, _ := jsstring.Lower(text(record, "content"))
					for _, t := range tokens {
						if strings.Contains(content, t) {
							n++
						}
					}
					return n
				}
				ranked := append([]*Object{}, metadata...)
				sort.SliceStable(ranked, func(i, j int) bool { return score(ranked[i]) > score(ranked[j]) })
				joined := append([]*Object{}, ranked[:min(2, len(ranked))]...)
				joined = append(joined, hits...)
				joined = append(joined, ranked[min(2, len(ranked)):]...)
				choices := []*Object{}
				positions := map[string]int{}
				for _, v := range joined {
					id := text(v, "id")
					if i, ok := positions[id]; ok {
						choices[i] = v
					} else {
						positions[id] = len(choices)
						choices = append(choices, v)
					}
				}
				if len(choices) > 4 {
					capped = true
				}
				for _, record := range choices[:min(4, len(choices))] {
					id := text(record, "id")
					if (!groupIDs[id] && len(groupIDs) >= 8) || (selected[id] == nil && len(selected) >= 16) {
						capped = true
						continue
					}
					groupIDs[id] = true
					entry := selected[id]
					if entry == nil {
						sanitized, e := a.Sanitize(record)
						if e != nil {
							return e
						}
						entry = epObj("memory", sanitized, "candidateIndexes", []any{})
						selected[id] = entry
						out.Targets = append(out.Targets, entry)
					}
					indexes := epArr(value(entry, "candidateIndexes"))
					index := value(c, "candidateIndex")
					exists := false
					for _, v := range indexes {
						if v == index {
							exists = true
						}
					}
					if !exists {
						_ = entry.Set("candidateIndexes", append(indexes, index))
					}
				}
			}
		}
		return nil
	})
	if capped {
		out.Coverage = "capped"
	}
	return
}
