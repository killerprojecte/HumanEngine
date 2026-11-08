package semantic

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sort"
)

const AlgorithmVersion = 1
const PromptVersion = "original-declarations-v1"

type Dirty struct {
	SceneID       string
	PersonID      *string
	EpisodeAnchor *jsjson.Object
	Trigger       *jsjson.Object
	Reason        string
}
type InputEntry struct{ Selection, Assertion *jsjson.Object }
type ResolvedInput struct {
	Coverage                                 string
	PartitionKey                             *string
	EpisodeAnchor                            *jsjson.Object
	EpisodeMembershipKey                     *string
	AnchorDependencies, AnchorQualifications []any
	Entries                                  []InputEntry
}

// Input's ordered JSON retains the original authorized bodies, not re-resolved replacements.
type Input struct{ Value *jsjson.Object }

func (s *Input) SceneID() string   { return semString(s.Value, "sceneId") }
func (s *Input) Key() string       { return semString(s.Value, "inputKey") }
func (s *Input) Assertions() []any { return scArr(scGet(s.Value, "assertions")) }
func (s *Input) Selections() []any { return scArr(scGet(s.Value, "selections")) }
func (s *Input) Clone() *Input     { return &Input{s.Value.Clone()} }
func inputObject(pairs ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(pairs); i += 2 {
		_ = o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}
func nullableString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
func nullableObject(p *jsjson.Object) any {
	if p == nil {
		return nil
	}
	return p
}
func nullableArray(a []any) any {
	if a == nil {
		return nil
	}
	return a
}
func inputHash(v any) bool { _, e := scHash(v); return e == nil }

// CaptureInput refuses incomplete partitions and never truncates declarations.
func CaptureInput(scene string, r ResolvedInput) *Input {
	if r.Coverage != "complete" || len(r.Entries) < 2 || len(r.Entries) > 12 {
		return nil
	}
	entries := append([]InputEntry(nil), r.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		return inputLess(semString(scObject(scGet(entries[i].Selection, "ref")), "id"), semString(scObject(scGet(entries[j].Selection, "ref")), "id"))
	})
	ids := map[string]bool{}
	people := map[any]bool{}
	roots := map[string]bool{}
	selections, assertions := []any{}, []any{}
	for _, e := range entries {
		if _, err := scSelection(e.Selection); err != nil {
			return nil
		}
		ref := scObject(scGet(e.Selection, "ref"))
		id := semString(ref, "id")
		if ids[id] {
			return nil
		}
		ids[id] = true
		a := e.Assertion
		record := scObject(scGet(a, "record"))
		scope := scObject(scGet(record, "scope"))
		if scGet(a, "derived") == true || scGet(record, "internalUse") == true || scGet(record, "id") != id || scGet(scope, "kind") != "scene" || scGet(scope, "sceneId") != scene || scGet(a, "dependencyKey") != scGet(e.Selection, "dependencyKey") || scGet(a, "rootCoverage") != "complete" || len(scArr(scGet(a, "proofs"))) == 0 || len(scArr(scGet(a, "roots"))) == 0 {
			return nil
		}
		person := scGet(scope, "personId")
		if person != nil {
			if _, ok := person.(string); !ok {
				return nil
			}
		}
		people[person] = true
		for _, root := range scArr(scGet(a, "roots")) {
			if !inputHash(root) {
				return nil
			}
			roots[root.(string)] = true
		}
		selections = append(selections, e.Selection.Clone())
		assertions = append(assertions, a.Clone())
	}
	anchor := r.EpisodeAnchor
	if anchor != nil {
		if semString(anchor, "id") == "" {
			return nil
		}
		if _, e := scSafe(1)(scGet(anchor, "version")); e != nil {
			return nil
		}
		if !inputHash(nullableString(r.EpisodeMembershipKey)) || len(r.AnchorDependencies) == 0 || len(r.AnchorQualifications) == 0 {
			return nil
		}
	} else if len(people) != 1 || !inputHash(nullableString(r.PartitionKey)) {
		return nil
	}
	if len(roots) < 2 {
		return nil
	}
	rootKeys := []string{}
	for k := range roots {
		rootKeys = append(rootKeys, k)
	}
	sort.Strings(rootKeys)
	rootValues := []any{}
	for _, k := range rootKeys {
		rootValues = append(rootValues, k)
	}
	var person any
	if len(people) == 1 {
		for p := range people {
			person = p
		}
	}
	partition := nullableString(r.PartitionKey)
	if anchor != nil {
		partition = nil
	}
	o := inputObject("sceneId", scene, "personId", person, "episodeOnly", anchor != nil, "partitionKey", partition)
	if anchor != nil {
		_ = o.Set("episodeAnchor", anchor.Clone())
		_ = o.Set("episodeMembershipKey", nullableString(r.EpisodeMembershipKey))
		_ = o.Set("anchorDependencies", r.AnchorDependencies)
		_ = o.Set("anchorQualifications", r.AnchorQualifications)
	}
	_ = o.Set("selections", selections)
	_ = o.Set("assertions", assertions)
	_ = o.Set("roots", rootValues)
	key, err := privacyHash([]any{float64(AlgorithmVersion), PromptVersion, scene, selections, assertions, nullableObject(anchor), nullableString(r.EpisodeMembershipKey), nullableArray(r.AnchorDependencies), nullableArray(r.AnchorQualifications), partition})
	if err != nil {
		return nil
	}
	_ = o.Set("inputKey", key)
	return &Input{o.Clone()}
}
func InputUnchanged(snapshot *Input, r ResolvedInput) bool {
	if snapshot == nil {
		return false
	}
	current := CaptureInput(snapshot.SceneID(), r)
	return current != nil && current.Key() == snapshot.Key() && privacyEqual(current.Value, snapshot.Value)
}
func ValidateProposal(v *jsjson.Object) (*jsjson.Object, error) {
	o, e := scShape(v, scF("kind", scEnumeration("summary_candidate", "duplicate_candidate", "episode_candidate")), scF("selectedParentIds", scList(scIdValue, 2, 12)), scF("text", scBounded(1, 6000)), scOpt("episodeAnchor", scAnchor), scF("hypothesis", scBoolean))
	if e != nil {
		return nil, e
	}
	_, has := o.Get("episodeAnchor")
	if (scGet(o, "kind") == "episode_candidate") != has {
		return nil, ErrContract
	}
	seen := map[string]bool{}
	for _, v := range scArr(scGet(o, "selectedParentIds")) {
		id := v.(string)
		if seen[id] {
			return nil, ErrContract
		}
		seen[id] = true
	}
	return o, nil
}

func inputLess(a, b string) bool {
	x, _ := jsjson.UTF16(a)
	y, _ := jsjson.UTF16(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}
