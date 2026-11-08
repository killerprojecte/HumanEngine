package knowledge

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type Context struct {
	SceneID         string
	Simulation      bool
	Provenance      string
	ExposedEntities []any
}
type Reference = commitments.Reference
type Assertion = commitments.Assertion

type readStorage interface {
	Rows(*string, *int) ([]*Object, error)
	Get(string) (*Object, error)
	Entity(string, string) (*Object, error)
}

// Reader is invocation-local. Only ReadWorkingSetCurrent constructs production
// readers, borrowing the exact native publication or active write child.
type Reader struct {
	storage readStorage
	ready   func() error
	current func(Context, Reference) (*Assertion, error)
	enabled func(Context) (bool, error)
	now     func() int64
	dates   *jsdate.Parser
}

func (r *Reader) AssertReady() error { return r.ready() }
func referenceValue(v any) Reference {
	return Reference{ID: str(get(v, "id")), Version: num(get(v, "version")), DeletionEpoch: num(get(v, "deletionEpoch"))}
}
func sameReadRef(a, b any) bool {
	return get(a, "id") == get(b, "id") && get(a, "version") == get(b, "version") && get(a, "deletionEpoch") == get(b, "deletionEpoch")
}
func BackingFingerprint(record *Object) (string, error) {
	return digest([]any{get(record, "id"), get(record, "scope"), get(record, "content"), get(record, "kind"), get(record, "epistemicKind"), get(record, "occurredAt"), get(record, "validFrom"), get(record, "validUntil"), get(record, "internalUse")})
}
func intersectReadProofs(stored, live []any) ([]any, error) {
	out := []any{}
	for _, p := range stored {
		for _, q := range live {
			if get(p, "proofId") != get(q, "proofId") {
				continue
			}
			pk, e := assertions.CanonicalProofKey(object(p))
			if e != nil {
				return nil, e
			}
			qk, e := assertions.CanonicalProofKey(object(q))
			if e != nil {
				return nil, e
			}
			if pk == qk && deepEqual(p, q) {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}
func (r *Reader) Current(ctx Context, reference Reference) (*Assertion, error) {
	if e := r.ready(); e != nil {
		return nil, e
	}
	v, e := r.current(ctx, reference)
	if e != nil || v == nil {
		return nil, e
	}
	if get(v.Record, "id") != reference.ID || get(get(v.Record, "scope"), "kind") != "scene" || get(get(v.Record, "scope"), "sceneId") != ctx.SceneID || len(v.Proofs) == 0 {
		return nil, nil
	}
	return v, nil
}
func personal(record *Object) bool {
	return (get(get(record, "scope"), "kind") == "scene" && truthy(get(get(record, "scope"), "personId"))) || !contains([]string{"fact", "claim", "term"}, str(get(record, "kind")))
}

type readStage struct {
	remaining int
	memo      map[string]*Object
}

func newReadStage() *readStage { return &readStage{512, map[string]*Object{}} }

var errDependencyLimit = errors.New("knowledge_dependency_limit")

func (r *Reader) Project(ctx Context, edge *Object) (*Object, error) {
	if e := r.ready(); e != nil {
		return nil, e
	}
	return r.project(ctx, edge, map[string]bool{}, newReadStage())
}
func (r *Reader) project(ctx Context, edge *Object, visiting map[string]bool, stage *readStage) (*Object, error) {
	if ctx.Simulation || get(edge, "sceneId") != ctx.SceneID {
		return nil, nil
	}
	enabled, e := r.enabled(ctx)
	if e != nil || !enabled {
		return nil, e
	}
	id := str(get(edge, "id"))
	if visiting[id] {
		return nil, nil
	}
	if len(visiting) >= 64 {
		return nil, errDependencyLimit
	}
	stage.remaining--
	if stage.remaining < 0 {
		return nil, errDependencyLimit
	}
	backing, e := r.Current(ctx, referenceValue(get(edge, "backing")))
	if e != nil || backing == nil {
		return nil, e
	}
	fingerprint, e := BackingFingerprint(backing.Record)
	if e != nil {
		return nil, e
	}
	if fingerprint != get(edge, "backingFingerprint") {
		return nil, nil
	}
	proofs, e := intersectReadProofs(arr(get(edge, "proofs")), backing.Proofs)
	if e != nil || len(proofs) == 0 {
		return nil, e
	}
	if cached := stage.memo[id]; cached != nil {
		return cached, nil
	}
	requires := personal(backing.Record) || get(get(edge, "subject"), "kind") == "person" || get(get(edge, "object"), "kind") == "person"
	path := map[string]bool{}
	for k, v := range visiting {
		path[k] = v
	}
	path[id] = true
	dependencies, statuses, readDependencies := []any{}, []any{}, []any{}
	for _, dep := range arr(get(edge, "dependencies")) {
		entity, e := r.storage.Entity(str(get(get(dep, "entity"), "id")), ctx.SceneID)
		if e != nil {
			return nil, e
		}
		view, e := r.Current(ctx, referenceValue(get(get(dep, "entity"), "backing")))
		if e != nil {
			return nil, e
		}
		if entity == nil || !deepEqual(entity, get(dep, "entity")) || view == nil {
			return nil, nil
		}
		fp, e := BackingFingerprint(view.Record)
		if e != nil {
			return nil, e
		}
		if fp != get(dep, "backingFingerprint") {
			return nil, nil
		}
		kept, e := intersectReadProofs(arr(get(dep, "proofs")), view.Proofs)
		if e != nil {
			return nil, e
		}
		origin, e := r.storage.Get(str(get(entity, "originEdgeId")))
		if e != nil {
			return nil, e
		}
		if len(kept) == 0 || origin == nil {
			return nil, nil
		}
		projected, e := r.project(ctx, origin, path, stage)
		if e != nil || projected == nil {
			return nil, e
		}
		requires = requires || personal(view.Record) || truthy(get(projected, "requiresPersonalization"))
		d := object(clone(dep))
		_ = d.Set("proofs", kept)
		dependencies = append(dependencies, d)
		statuses = append(statuses, obj("entityId", get(get(dep, "entity"), "id"), "status", get(view.Record, "status")))
	}
	for _, dep := range arr(get(edge, "readDependencies")) {
		stage.remaining--
		if stage.remaining < 0 {
			return nil, errDependencyLimit
		}
		view, e := r.Current(ctx, referenceValue(get(dep, "backing")))
		if e != nil || view == nil {
			return nil, e
		}
		fp, e := BackingFingerprint(view.Record)
		if e != nil {
			return nil, e
		}
		if fp != get(dep, "backingFingerprint") {
			return nil, nil
		}
		requires = requires || personal(view.Record)
		currentProofs := view.Proofs
		if has(dep, "edgeId") {
			origin, e := r.storage.Get(str(get(dep, "edgeId")))
			if e != nil {
				return nil, e
			}
			if origin == nil || get(origin, "version") != get(dep, "edgeVersion") || !sameReadRef(get(origin, "backing"), get(dep, "backing")) || get(origin, "backingFingerprint") != get(dep, "backingFingerprint") {
				return nil, nil
			}
			projected, e := r.project(ctx, origin, path, stage)
			if e != nil || projected == nil {
				return nil, e
			}
			requires = requires || truthy(get(projected, "requiresPersonalization"))
			currentProofs = arr(get(projected, "proofs"))
		}
		kept, e := intersectReadProofs(arr(get(dep, "proofs")), currentProofs)
		if e != nil || len(kept) == 0 {
			return nil, e
		}
		d := object(clone(dep))
		_ = d.Set("proofs", kept)
		readDependencies = append(readDependencies, d)
	}
	spans := []any{}
	for _, span := range arr(get(edge, "spans")) {
		found := false
		for _, p := range proofs {
			for _, s := range arr(get(p, "sources")) {
				if deepEqual(s, get(span, "source")) {
					found = true
				}
			}
		}
		if found {
			spans = append(spans, span)
		}
	}
	if len(spans) == 0 {
		return nil, nil
	}
	applicability := "current"
	expired, future := false, false
	if truthy(get(edge, "validUntil")) {
		ms, valid, e := r.dates.ParseMillis(str(get(edge, "validUntil")))
		if e != nil {
			return nil, e
		}
		expired = valid && r.now() >= ms
	}
	if !expired && truthy(get(edge, "validFrom")) {
		ms, valid, e := r.dates.ParseMillis(str(get(edge, "validFrom")))
		if e != nil {
			return nil, e
		}
		future = valid && r.now() < ms
	}
	if expired {
		applicability = "expired"
	} else if future {
		applicability = "future"
	} else if contains([]string{"past", "future", "unspecified"}, str(get(edge, "temporalScope"))) {
		applicability = str(get(edge, "temporalScope"))
	}
	result := object(clone(edge))
	for _, m := range obj("proofs", proofs, "spans", spans, "dependencies", dependencies, "readDependencies", readDependencies, "applicability", applicability, "backingStatus", get(backing.Record, "status"), "requiresPersonalization", requires, "dependencyStatuses", statuses).Members() {
		_ = result.Set(m.Key, clone(m.Value))
	}
	stage.memo[id] = result
	return result, nil
}
func nodeKey(endpoint any) (string, error) {
	switch get(endpoint, "kind") {
	case "person":
		return "person:" + str(get(endpoint, "personId")), nil
	case "entity":
		return "entity:" + str(get(get(endpoint, "entity"), "id")), nil
	}
	raw, e := jsjson.Stringify(endpoint)
	return "literal:" + raw, e
}
func conflictKey(edge any) (string, error) {
	node, e := nodeKey(get(edge, "subject"))
	if e != nil {
		return "", e
	}
	return jsjson.Stringify([]any{node, get(edge, "predicate"), get(edge, "property")})
}
func emptyQuery(reason string) *Object {
	o := obj("edges", []any{}, "paths", []any{}, "supportClosure", obj("edges", []any{}, "records", []any{}), "coverage", "complete", "interpretation", "attributed_claims_not_transitive_truth")
	if reason != "" {
		_ = o.Set("coverage", "limited")
		_ = o.Set("reason", reason)
	}
	return o
}

// Query mirrors the internal KnowledgeReader API. ParseQuery is the strict
// external schema boundary, including optional memory anchors for tool calls.
func (r *Reader) Query(ctx Context, input *Object) (*Object, error) {
	if e := r.ready(); e != nil {
		return nil, e
	}
	// The internal source API dereferences anchor before simulation/settings gates.
	// Preserve that ordering; external callers still pass through ParseQuery.
	if input == nil {
		return nil, errors.New("Cannot read properties of null (reading 'anchor')")
	}
	anchor, present := input.Get("anchor")
	if !present {
		return nil, errors.New("Cannot read properties of undefined (reading 'kind')")
	}
	if anchor == nil {
		return nil, errors.New("Cannot read properties of null (reading 'kind')")
	}
	if get(anchor, "kind") == "entity" {
		exposed := false
		for _, v := range ctx.ExposedEntities {
			if get(v, "entityId") == get(anchor, "entityId") && sameReadRef(get(v, "backingRef"), get(anchor, "backingRef")) {
				exposed = true
				break
			}
		}
		if !exposed {
			return nil, errors.New("knowledge_anchor_not_exposed")
		}
	}
	return r.queryFromRoots(ctx, input, func(live []*Object) ([]string, error) {
		if get(anchor, "kind") == "person" {
			return []string{"person:" + str(get(anchor, "personId"))}, nil
		}
		for _, edge := range live {
			for _, point := range []any{get(edge, "subject"), get(edge, "object")} {
				entity := get(point, "entity")
				if get(point, "kind") == "entity" && get(entity, "id") == get(anchor, "entityId") && get(entity, "originEdgeId") == get(edge, "id") && sameReadRef(get(entity, "backing"), get(anchor, "backingRef")) {
					return []string{"entity:" + str(get(anchor, "entityId"))}, nil
				}
			}
		}
		return nil, errors.New("knowledge_anchor_unavailable")
	})
}
func (r *Reader) Discover(ctx Context, exactBacking *Object, input *Object) (*Object, error) {
	if e := r.ready(); e != nil {
		return nil, e
	}
	if _, e := ref(exactBacking); e != nil {
		return nil, errors.New("knowledge_reference_invalid")
	}
	if !ctx.Simulation {
		enabled, e := r.enabled(ctx)
		if e != nil {
			return nil, e
		}
		if enabled {
			v, e := r.Current(ctx, referenceValue(exactBacking))
			if e != nil {
				return nil, e
			}
			if v == nil {
				return nil, errors.New("knowledge_backing_unavailable")
			}
		}
	}
	return r.queryFromRoots(ctx, input, func(live []*Object) ([]string, error) {
		roots := []string{}
		for _, edge := range live {
			if !sameReadRef(get(edge, "backing"), exactBacking) {
				continue
			}
			for _, point := range []any{get(edge, "subject"), get(edge, "object")} {
				entity := get(point, "entity")
				if get(point, "kind") == "entity" && get(entity, "originEdgeId") == get(edge, "id") && sameReadRef(get(entity, "backing"), exactBacking) {
					roots = append(roots, "entity:"+str(get(entity, "id")))
				}
			}
		}
		return roots, nil
	})
}
func (r *Reader) queryFromRoots(ctx Context, input *Object, resolve func([]*Object) ([]string, error)) (*Object, error) {
	if e := r.ready(); e != nil {
		return nil, e
	}
	if ctx.Simulation {
		return emptyQuery(""), nil
	}
	enabled, e := r.enabled(ctx)
	if e != nil {
		return nil, e
	}
	if !enabled {
		return emptyQuery(""), nil
	}
	predicates := arr(get(input, "predicates"))
	depth := num(get(input, "depth"))
	if depth != 1 && depth != 2 || len(predicates) == 0 || len(predicates) > 7 {
		return nil, errors.New("knowledge_query_invalid")
	}
	for _, p := range predicates {
		if !contains(predicateNames, str(p)) {
			return nil, errors.New("knowledge_query_invalid")
		}
	}
	limit := 201
	rows, e := r.storage.Rows(&ctx.SceneID, &limit)
	if e != nil {
		return nil, e
	}
	if len(rows) > 200 {
		return emptyQuery("scan_limit"), nil
	}
	stage := newReadStage()
	live := []*Object{}
	for _, edge := range rows {
		p, e := r.project(ctx, edge, map[string]bool{}, stage)
		if errors.Is(e, errDependencyLimit) {
			return emptyQuery("dependency_limit"), nil
		}
		if e != nil {
			return nil, e
		}
		if p != nil {
			live = append(live, p)
		}
	}
	roots, e := resolve(live)
	if e != nil {
		return nil, e
	}
	unique := []string{}
	seenRoots := map[string]bool{}
	for _, s := range roots {
		if !seenRoots[s] {
			seenRoots[s] = true
			unique = append(unique, s)
		}
	}
	if len(unique) > 12 {
		return emptyQuery("edge_limit"), nil
	}
	type step struct {
		node string
		path []any
	}
	frontier := []step{}
	for _, s := range unique {
		frontier = append(frontier, step{s, []any{}})
	}
	selected := map[string]*Object{}
	selectedOrder := []*Object{}
	paths := []any{}
	pathKeys := map[string]bool{}
	for hop := 0; hop < int(depth); hop++ {
		next := []step{}
		for _, s := range frontier {
			matches := []*Object{}
			buckets := map[string]bool{}
			for _, edge := range live {
				included := false
				for _, p := range predicates {
					if p == get(edge, "predicate") {
						included = true
					}
				}
				if !included {
					continue
				}
				a, e := nodeKey(get(edge, "subject"))
				if e != nil {
					return nil, e
				}
				b, e := nodeKey(get(edge, "object"))
				if e != nil {
					return nil, e
				}
				if a == s.node || b == s.node {
					matches = append(matches, edge)
					key, e := conflictKey(edge)
					if e != nil {
						return nil, e
					}
					buckets[key] = true
				}
			}
			group := []*Object{}
			for _, edge := range live {
				key, e := conflictKey(edge)
				if e != nil {
					return nil, e
				}
				if buckets[key] {
					group = append(group, edge)
				}
			}
			if len(group) > 12 {
				return emptyQuery("edge_limit"), nil
			}
			for _, edge := range group {
				id := str(get(edge, "id"))
				if _, ok := selected[id]; !ok {
					selectedOrder = append(selectedOrder, edge)
				}
				selected[id] = edge
			}
			for _, edge := range matches {
				id := str(get(edge, "id"))
				cycle := false
				for _, p := range s.path {
					if p == id {
						cycle = true
					}
				}
				if cycle {
					continue
				}
				path := append(append([]any{}, s.path...), id)
				key, e := jsjson.Stringify(path)
				if e != nil {
					return nil, e
				}
				if !pathKeys[key] {
					pathKeys[key] = true
					paths = append(paths, path)
				}
				for _, point := range []any{get(edge, "subject"), get(edge, "object")} {
					key, e := nodeKey(point)
					if e != nil {
						return nil, e
					}
					if get(point, "kind") != "literal" && key != s.node {
						next = append(next, step{key, path})
					}
				}
			}
			if len(selected) > 40 {
				return emptyQuery("edge_limit"), nil
			}
			if len(paths) > 64 {
				return emptyQuery("path_limit"), nil
			}
		}
		frontier = next
	}
	closure := map[string]*Object{}
	closureOrder := []any{}
	records := map[string]bool{}
	recordOrder := []any{}
	visited := map[string]bool{}
	queue := append([]*Object{}, selectedOrder...)
	retain := func(reference any, fingerprint any) (bool, error) {
		key, e := jsjson.Stringify(reference)
		if e != nil {
			return false, e
		}
		if records[key] {
			return true, nil
		}
		current, e := r.Current(ctx, referenceValue(reference))
		if e != nil || current == nil {
			return false, e
		}
		fp, e := BackingFingerprint(current.Record)
		if e != nil {
			return false, e
		}
		if fp != fingerprint {
			return false, nil
		}
		records[key] = true
		recordOrder = append(recordOrder, obj("ref", clone(reference), "record", clone(current.Record)))
		return len(records) <= 128, nil
	}
	for len(queue) > 0 {
		edge := queue[0]
		queue = queue[1:]
		id := str(get(edge, "id"))
		if visited[id] {
			continue
		}
		visited[id] = true
		if len(visited) > 40 {
			return emptyQuery("dependency_limit"), nil
		}
		ok, e := retain(get(edge, "backing"), get(edge, "backingFingerprint"))
		if e != nil {
			return nil, e
		}
		if !ok {
			return emptyQuery("dependency_limit"), nil
		}
		origins := []string{}
		for _, dep := range arr(get(edge, "dependencies")) {
			ok, e := retain(get(get(dep, "entity"), "backing"), get(dep, "backingFingerprint"))
			if e != nil {
				return nil, e
			}
			if !ok {
				return emptyQuery("dependency_limit"), nil
			}
			origins = append(origins, str(get(get(dep, "entity"), "originEdgeId")))
		}
		for _, dep := range arr(get(edge, "readDependencies")) {
			ok, e := retain(get(dep, "backing"), get(dep, "backingFingerprint"))
			if e != nil {
				return nil, e
			}
			if !ok {
				return emptyQuery("dependency_limit"), nil
			}
			if has(dep, "edgeId") {
				origins = append(origins, str(get(dep, "edgeId")))
			}
		}
		for _, id := range origins {
			origin := stage.memo[id]
			if origin == nil {
				return emptyQuery("dependency_limit"), nil
			}
			if selected[id] == nil {
				if closure[id] == nil {
					closureOrder = append(closureOrder, origin)
				}
				closure[id] = origin
			}
			if len(selected)+len(closure) > 40 {
				return emptyQuery("dependency_limit"), nil
			}
			if !visited[id] {
				queue = append(queue, origin)
			}
		}
	}
	edges := []any{}
	for _, v := range selectedOrder {
		edges = append(edges, selected[str(get(v, "id"))])
	}
	return obj("edges", edges, "paths", paths, "supportClosure", obj("edges", closureOrder, "records", recordOrder), "coverage", "complete", "interpretation", "attributed_claims_not_transitive_truth"), nil
}

var predicateNames = []string{"participates_in", "prefers_topic", "uses_tool", "member_of", "part_of", "depends_on", "has_property"}

func ParseQuery(v any, tool bool) (*Object, error) {
	anchor := func(v any) (any, error) {
		switch get(v, "kind") {
		case "person":
			return shape(v, f("kind", enumeration("person")), f("personId", idValue))
		case "entity":
			return shape(v, f("kind", enumeration("entity")), f("entityId", idValue), f("backingRef", ref))
		case "memory":
			if tool {
				return shape(v, f("kind", enumeration("memory")), f("memoryId", idValue))
			}
		}
		return nil, ErrSchema
	}
	depth := func(v any) (any, error) {
		if n, ok := v.(float64); ok && (n == 1 || n == 2) {
			return n, nil
		}
		return nil, ErrSchema
	}
	return shape(v, f("anchor", anchor), f("predicates", list(enumeration(predicateNames...), 1, 7)), f("depth", depth))
}
