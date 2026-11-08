package memoryservice

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

// SemanticEpisodes uses one authentic read owner for knowledge, provenance and
// anchor qualification. The options are captured real settings, never defaults.
// NotificationPorts.Episodes adapts this result's groups; no empty fallback.
func (s *Service) SemanticEpisodes(ctx context.Context, c Context, ref Reference, options knowledge.ReadOptions) (out *Object, err error) {
	err = s.d.WorkingSet.ReadScope(ctx, func(scope *workingset.Scope) error {
		return s.SemanticEpisodesInScope(ctx, scope, c, ref, options, func(result *Object) error { out = result; return nil })
	})
	return
}

// SemanticEpisodesInScope borrows an already active scope without opening a
// second publication or write child. Both stores enforce exact native binding.
func (s *Service) SemanticEpisodesInScope(ctx context.Context, scope *workingset.Scope, c Context, ref Reference, options knowledge.ReadOptions, fn func(*Object) error) error {
	if fn == nil {
		return errors.New("semantic_episode_callback_required")
	}
	return s.d.Knowledge.Storage.ReadWorkingSetEpisode(ctx, scope, s.d.Assertions, s.d.Commitments, options, func(reader *knowledge.Reader, current *commitments.ReadService) error {
		p := episodePorts{
			query:   reader.Query,
			current: func(c Context, ref *Object) (*Object, error) { return semanticCurrent(current, c, ref) },
			anchor: func(c Context, ref *Object) (*commitments.Assertion, error) {
				return current.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, semanticRef(ref))
			},
		}
		result, e := p.discover(c, refObject(ref))
		if e != nil {
			return e
		}
		return fn(result)
	})
}

// The pure source algorithm is private. Production ports above are concrete
// scoped owners; test callbacks only supply differential oracle inputs.
type episodePorts struct {
	query   func(knowledge.Context, *Object) (*Object, error)
	current func(Context, *Object) (*Object, error)
	anchor  func(Context, *Object) (*commitments.Assertion, error)
}

func epObj(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func epGet(v any, k string) any { return value(object(v), k) }
func epArr(v any) []any         { a, _ := v.([]any); return a }
func epEqual(a, b any) bool {
	switch x := a.(type) {
	case *Object:
		y, ok := b.(*Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, m := range x.Members() {
			v, ok := y.Get(m.Key)
			if !ok || !epEqual(m.Value, v) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !epEqual(v, y[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}
func epLess(a, b string) bool {
	x, _ := jsjson.UTF16(a)
	y, _ := jsjson.UTF16(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}
func epUnique(refs []any) ([]any, error) {
	out := []any{}
	indexes := map[string]int{}
	for _, ref := range refs {
		k, e := jsjson.Stringify(ref)
		if e != nil {
			return nil, e
		}
		if i, ok := indexes[k]; ok {
			out[i] = ref
		} else {
			indexes[k] = len(out)
			out = append(out, ref)
		}
	}
	return out, nil
}
func epEmpty(reason string) *Object {
	o := epObj("groups", []any{}, "coverage", "complete")
	if reason != "" {
		_ = o.Set("coverage", "limited")
		_ = o.Set("reason", reason)
	}
	return o
}
func epContext(c Context) knowledge.Context {
	return knowledge.Context{SceneID: c.SceneID, Simulation: c.Simulation, Provenance: "contextual"}
}
func epQuery(anchor *Object) *Object {
	return epObj("anchor", anchor, "predicates", []any{"participates_in"}, "depth", float64(1))
}
func (p episodePorts) record(c Context, ref *Object) (*Object, error) {
	a, e := p.anchor(c, ref)
	if e != nil {
		return nil, e
	}
	if a == nil {
		a, e = p.anchor(c, ref)
	}
	if e != nil || a == nil {
		return nil, e
	}
	return a.Record, nil
}
func (p episodePorts) discover(c Context, trigger *Object) (*Object, error) {
	if c.Simulation {
		return epEmpty(""), nil
	}
	current, e := p.current(c, trigger)
	if e != nil {
		return nil, e
	}
	record := object(value(current, "record"))
	if record == nil {
		record, e = p.record(c, trigger)
		if e != nil {
			return nil, e
		}
	}
	scope := object(value(record, "scope"))
	if record == nil || text(record, "id") != text(trigger, "id") || text(scope, "kind") != "scene" || text(scope, "sceneId") != c.SceneID || !truthy(value(scope, "personId")) {
		return epEmpty(""), nil
	}
	first, e := p.query(epContext(c), epQuery(epObj("kind", "person", "personId", value(scope, "personId"))))
	if e != nil {
		return nil, e
	}
	if text(first, "coverage") != "complete" {
		return epEmpty("knowledge_limited"), nil
	}
	entities := []*Object{}
	seen := map[string]*Object{}
	indexes := map[string]int{}
	for _, raw := range epArr(value(first, "edges")) {
		edge := object(raw)
		subject := object(value(edge, "subject"))
		obj := object(value(edge, "object"))
		entity := object(value(obj, "entity"))
		if text(edge, "predicate") != "participates_in" || text(subject, "kind") != "person" || !epEqual(value(subject, "personId"), value(scope, "personId")) || text(obj, "kind") != "entity" || text(entity, "kind") != "event" {
			continue
		}
		id := text(entity, "id")
		if previous, ok := seen[id]; ok {
			if !epEqual(previous, entity) {
				return epEmpty("knowledge_limited"), nil
			}
			entities[indexes[id]] = entity
		} else {
			indexes[id] = len(entities)
			entities = append(entities, entity)
		}
		seen[id] = entity
	}
	if len(entities) > 4 {
		return epEmpty("group_limit"), nil
	}
	groups := []any{}
	for _, entity := range entities {
		kc := epContext(c)
		kc.ExposedEntities = []any{epObj("entityId", value(entity, "id"), "backingRef", value(entity, "backing"))}
		page, e := p.query(kc, epQuery(epObj("kind", "entity", "entityId", value(entity, "id"), "backingRef", value(entity, "backing"))))
		if e != nil {
			return nil, e
		}
		if text(page, "coverage") != "complete" {
			return epEmpty("knowledge_limited"), nil
		}
		group, limited, e := p.group(c, entity, page)
		if e != nil {
			return nil, e
		}
		if limited {
			return epEmpty("declaration_unavailable"), nil
		}
		if group == nil {
			continue
		}
		keep := false
		for _, parent := range epArr(value(group, "parents")) {
			if epEqual(epGet(parent, "ref"), trigger) {
				keep = true
				break
			}
		}
		if !keep && truthy(value(record, "internalUse")) {
			for _, edge := range epArr(value(group, "edges")) {
				if epEqual(epGet(edge, "backing"), trigger) {
					keep = true
					break
				}
			}
		}
		if keep {
			groups = append(groups, group)
		}
	}
	return epObj("groups", groups, "coverage", "complete"), nil
}
func epDirect(edge *Object) []any {
	refs := []any{value(edge, "backing")}
	for _, dep := range epArr(value(edge, "dependencies")) {
		refs = append(refs, epGet(epGet(dep, "entity"), "backing"))
	}
	for _, dep := range epArr(value(edge, "readDependencies")) {
		refs = append(refs, epGet(dep, "backing"))
	}
	return refs
}
func (p episodePorts) group(c Context, entity, page *Object) (*Object, bool, error) {
	declarations := []*Object{}
	for _, raw := range epArr(value(page, "edges")) {
		edge := object(raw)
		obj := object(value(edge, "object"))
		if text(edge, "predicate") == "participates_in" && text(obj, "kind") == "entity" && epEqual(value(obj, "entity"), entity) {
			declarations = append(declarations, edge)
		}
	}
	if len(declarations) < 2 {
		return nil, false, nil
	}
	all := map[string]*Object{}
	for _, raw := range append(append([]any{}, epArr(value(page, "edges"))...), epArr(epGet(value(page, "supportClosure"), "edges"))...) {
		edge := object(raw)
		all[text(edge, "id")] = edge
	}
	origin := all[text(entity, "originEdgeId")]
	if origin == nil || !epEqual(value(origin, "backing"), value(entity, "backing")) {
		return nil, true, nil
	}
	closure := []*Object{}
	seen := map[string]bool{}
	pending := append([]*Object{}, declarations...)
	for len(pending) > 0 {
		edge := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		id := text(edge, "id")
		if seen[id] {
			continue
		}
		if len(closure) >= 40 {
			return nil, true, nil
		}
		seen[id] = true
		closure = append(closure, edge)
		ids := []any{}
		for _, dep := range epArr(value(edge, "dependencies")) {
			ids = append(ids, epGet(epGet(dep, "entity"), "originEdgeId"))
		}
		for _, dep := range epArr(value(edge, "readDependencies")) {
			if truthy(epGet(dep, "edgeId")) {
				ids = append(ids, epGet(dep, "edgeId"))
			}
		}
		for _, rawID := range ids {
			id, _ := rawID.(string)
			required := all[id]
			if required == nil {
				return nil, true, nil
			}
			pending = append(pending, required)
		}
	}
	rawRefs := []any{}
	for _, edge := range closure {
		rawRefs = append(rawRefs, epDirect(edge)...)
	}
	refs, e := epUnique(rawRefs)
	if e != nil {
		return nil, false, e
	}
	records := map[string]*Object{}
	selections := map[string]*Object{}
	for _, raw := range refs {
		ref := object(raw)
		v, e := p.current(c, ref)
		if e != nil {
			return nil, false, e
		}
		if v == nil {
			record, e := p.record(c, ref)
			if e != nil {
				return nil, false, e
			}
			if record != nil && text(record, "id") == text(ref, "id") && truthy(value(record, "internalUse")) {
				continue
			}
			return nil, true, nil
		}
		record := object(value(v, "record"))
		if truthy(value(record, "internalUse")) {
			continue
		}
		if truthy(value(v, "derived")) {
			return nil, true, nil
		}
		scope := object(value(record, "scope"))
		if text(record, "id") != text(ref, "id") || text(scope, "kind") != "scene" || text(scope, "sceneId") != c.SceneID || text(v, "rootCoverage") != "complete" || len(epArr(value(v, "proofs"))) == 0 {
			return nil, true, nil
		}
		id := text(ref, "id")
		if old := selections[id]; old != nil && !epEqual(value(old, "ref"), ref) {
			return nil, true, nil
		}
		selections[id] = epObj("ref", ref.Clone(), "dependencyKey", value(v, "dependencyKey"))
		records[id] = v
	}
	if len(records) < 2 || len(records) > 12 {
		return nil, true, nil
	}
	for _, edge := range declarations {
		subject := object(value(edge, "subject"))
		if text(subject, "kind") != "person" {
			return nil, true, nil
		}
		direct, e := epUnique(epDirect(edge))
		if e != nil {
			return nil, false, e
		}
		matched := false
		for _, raw := range direct {
			ref := object(raw)
			selection := selections[text(ref, "id")]
			v := records[text(ref, "id")]
			scope := epGet(epGet(v, "record"), "scope")
			if selection != nil && epEqual(value(selection, "ref"), ref) && epGet(scope, "kind") == "scene" && epEqual(epGet(scope, "personId"), value(subject, "personId")) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, true, nil
		}
	}
	dependencyMap := map[string]*Object{}
	for _, edge := range closure {
		required := []*Object{epObj("ref", value(edge, "backing"), "fingerprint", value(edge, "backingFingerprint"), "proofs", value(edge, "proofs"))}
		for _, d := range epArr(value(edge, "dependencies")) {
			required = append(required, epObj("ref", epGet(epGet(d, "entity"), "backing"), "fingerprint", epGet(d, "backingFingerprint"), "proofs", epGet(d, "proofs")))
		}
		for _, d := range epArr(value(edge, "readDependencies")) {
			required = append(required, epObj("ref", epGet(d, "backing"), "fingerprint", epGet(d, "backingFingerprint"), "proofs", epGet(d, "proofs")))
		}
		for _, dependency := range required {
			ref := object(value(dependency, "ref"))
			live, e := p.anchor(c, ref)
			if e != nil {
				return nil, false, e
			}
			if live == nil {
				return nil, true, nil
			}
			scope := object(value(live.Record, "scope"))
			if text(live.Record, "id") != text(ref, "id") || text(scope, "kind") != "scene" || text(scope, "sceneId") != c.SceneID {
				return nil, true, nil
			}
			for _, proof := range epArr(value(dependency, "proofs")) {
				found := false
				for _, q := range live.Proofs {
					if epEqual(proof, q) {
						found = true
						break
					}
				}
				if !found {
					return nil, true, nil
				}
			}
			key, e := semanticHash(dependency)
			if e != nil {
				return nil, false, e
			}
			dependencyMap[key] = dependency.Clone()
			if len(dependencyMap) > 128 {
				return nil, true, nil
			}
		}
	}
	keys := []string{}
	for k := range dependencyMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	dependencies := []any{}
	for _, k := range keys {
		dependencies = append(dependencies, dependencyMap[k])
	}
	keys = []string{}
	for k := range records {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return epLess(keys[i], keys[j]) })
	parents := []any{}
	for _, k := range keys {
		parents = append(parents, selections[k])
	}
	sort.SliceStable(closure, func(i, j int) bool { return epLess(text(closure[i], "id"), text(closure[j], "id")) })
	edges := []any{}
	qualifications := []any{}
	for _, edge := range closure {
		edges = append(edges, edge.Clone())
		q := epObj("edgeId", value(edge, "id"), "edgeVersion", value(edge, "version"), "registryVersion", value(edge, "registryVersion"), "predicate", value(edge, "predicate"))
		if truthy(value(edge, "property")) {
			_ = q.Set("property", value(edge, "property"))
		}
		_ = q.Set("subject", epEndpoint(object(value(edge, "subject"))))
		_ = q.Set("object", epEndpoint(object(value(edge, "object"))))
		for _, k := range []string{"polarity", "modality", "temporalScope", "conditions"} {
			_ = q.Set(k, value(edge, k))
		}
		live, e := p.anchor(c, object(value(edge, "backing")))
		if e != nil {
			return nil, false, e
		}
		var occurred any
		if live != nil {
			occurred = value(live.Record, "occurredAt")
		}
		_ = q.Set("occurredAt", occurred)
		for _, k := range []string{"validFrom", "validUntil", "status", "admission"} {
			_ = q.Set(k, value(edge, k))
		}
		_ = q.Set("interpretation", "attributed_proposed_association_not_observed_event")
		q, e = semantic.ValidateAnchorQualification(q)
		if e != nil {
			return nil, false, e
		}
		qualifications = append(qualifications, q)
	}
	anchor := epObj("id", value(entity, "id"), "version", value(origin, "version"))
	membership, e := semanticHash([]any{anchor, parents, edges})
	if e != nil {
		return nil, false, e
	}
	return epObj("anchor", anchor, "anchorMembershipKey", membership, "parents", parents, "anchorDependencies", dependencies, "anchorQualifications", qualifications, "edges", edges, "interpretation", "grouped_declarations_not_observed_event"), false, nil
}
func epEndpoint(e *Object) *Object {
	if text(e, "kind") != "entity" {
		return e.Clone()
	}
	entity := object(value(e, "entity"))
	return epObj("kind", "entity", "entityId", value(entity, "id"), "entityKind", value(entity, "kind"), "backing", value(entity, "backing"), "originEdgeId", value(entity, "originEdgeId"))
}
func (p episodePorts) valid(c Context, anchor *Object, parents []any, membership *string) (bool, error) {
	if len(parents) < 2 || len(parents) > 12 {
		return false, nil
	}
	var group *Object
	for _, raw := range parents {
		page, e := p.discover(c, object(raw))
		if e != nil {
			return false, e
		}
		if text(page, "coverage") != "complete" {
			return false, nil
		}
		for _, g := range epArr(value(page, "groups")) {
			if epEqual(epGet(g, "anchor"), anchor) {
				group = object(g)
				break
			}
		}
		if group != nil {
			break
		}
	}
	if group == nil || (membership != nil && text(group, "anchorMembershipKey") != *membership) {
		return false, nil
	}
	wanted, e := epUnique(parents)
	if e != nil {
		return false, e
	}
	sort.SliceStable(wanted, func(i, j int) bool { return epLess(text(object(wanted[i]), "id"), text(object(wanted[j]), "id")) })
	actual := []any{}
	for _, v := range epArr(value(group, "parents")) {
		actual = append(actual, epGet(v, "ref"))
	}
	return len(wanted) == len(parents) && epEqual(wanted, actual), nil
}

// SemanticEpisodeGroups is the concrete NotificationPorts.Episodes result adapter.
func (s *Service) SemanticEpisodeGroups(ctx context.Context, c Context, ref Reference, options knowledge.ReadOptions) ([]*Object, error) {
	result, err := s.SemanticEpisodes(ctx, c, ref, options)
	if err != nil {
		return nil, err
	}
	groups := []*Object{}
	for _, v := range epArr(value(result, "groups")) {
		groups = append(groups, object(v))
	}
	return groups, nil
}
