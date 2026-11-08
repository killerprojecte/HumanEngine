package knowledge

import (
	"errors"
	"strings"
	"testing"
)

func testProof(id string, parents ...string) *Object {
	ps := []any{}
	for _, p := range parents {
		ps = append(ps, p)
	}
	return obj("proofId", id, "sources", []any{obj("sceneId", "s", "messageId", id, "senderId", "p")}, "parents", ps)
}
func testEdge(id string, proofs ...*Object) *Object {
	ps := []any{}
	spans := []any{}
	for _, p := range proofs {
		ps = append(ps, p)
		source := arr(get(p, "sources"))[0]
		root, _ := digest([]any{"s", get(source, "messageId")})
		spans = append(spans, obj("source", source, "rootId", root, "partIndex", float64(0), "start", float64(0), "end", float64(1), "spanHash", strings.Repeat("a", 64), "stance", "self"))
	}
	return obj("id", id, "version", float64(1), "sceneId", "s", "registryVersion", float64(1), "predicate", "prefers_topic", "subject", obj("kind", "person", "personId", "p"), "object", obj("kind", "literal", "valueType", "text", "value", "Synthetic"), "polarity", "positive", "modality", "asserted", "temporalScope", "current", "conditions", []any{}, "backing", obj("id", "backing-"+id, "version", float64(1), "deletionEpoch", float64(0)), "backingFingerprint", strings.Repeat("b", 64), "proofs", ps, "spans", spans, "dependencies", []any{}, "readDependencies", []any{}, "status", "accepted", "admission", "self_explicit_no_visible_conflict", "admissionPolicyVersion", float64(1), "createdAt", "2026-11-01T00:00:00.000Z")
}
func readDep(edge *Object, proofs ...*Object) *Object {
	ps := []any{}
	for _, p := range proofs {
		ps = append(ps, p)
	}
	return obj("edgeId", get(edge, "id"), "edgeVersion", get(edge, "version"), "backing", clone(get(edge, "backing")), "backingFingerprint", get(edge, "backingFingerprint"), "proofs", ps)
}

type memoryStorage struct {
	rows    []*Object
	effects []string
	fail    error
}

func (s *memoryStorage) RequireTransaction() error { return s.fail }
func (s *memoryStorage) Rows(scene *string, limit *int) ([]*Object, error) {
	if scene != nil || limit != nil {
		return nil, errors.New("purge unexpectedly scoped or capped")
	}
	return s.rows, nil
}
func (s *memoryStorage) Remove(id string) error {
	s.effects = append(s.effects, "remove:"+id)
	return nil
}
func (s *memoryStorage) Replace(edge *Object) error {
	s.effects = append(s.effects, "replace:"+str(get(edge, "id")))
	for i, e := range s.rows {
		if get(e, "id") == get(edge, "id") {
			s.rows[i] = edge
		}
	}
	return nil
}
func TestPurgePropagatesCapturedDependencies(t *testing.T) {
	p, q := testProof("p", "deleted"), testProof("q")
	a := testEdge("a", p, q)
	b := testEdge("b", q)
	c := testEdge("c", q)
	_ = b.Set("readDependencies", []any{readDep(a, p, q)})
	_ = c.Set("readDependencies", []any{readDep(b, q)})
	s := &memoryStorage{rows: []*Object{a, b, c}}
	l := &Lifecycle{s}
	if e := l.PurgeAssertion("deleted"); e != nil {
		t.Fatal(e)
	}
	if strings.Join(s.effects, ",") != "replace:a,replace:b,replace:c" {
		t.Fatal(s.effects)
	}
	for _, e := range s.rows {
		if get(e, "version") != float64(2) {
			t.Fatal("version", get(e, "id"))
		}
		if err := ValidateEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	if len(arr(get(s.rows[0], "spans"))) != 1 || get(arr(get(s.rows[1], "readDependencies"))[0], "edgeVersion") != float64(2) {
		t.Fatal("span or captured version")
	}
	if len(arr(get(a, "proofs"))) != 2 || get(a, "version") != float64(1) {
		t.Fatal("mutated input")
	}
}
func TestPurgeDoesNotRebindHistoricalRead(t *testing.T) {
	for _, field := range []string{"edgeVersion", "backing", "backingFingerprint"} {
		t.Run(field, func(t *testing.T) {
			p, q := testProof("p", "deleted"), testProof("q")
			a := testEdge("a", p, q)
			b := testEdge("b", q)
			d := readDep(a, q)
			switch field {
			case "edgeVersion":
				_ = d.Set(field, float64(9))
			case "backing":
				_ = d.Set(field, obj("id", "older", "version", float64(1), "deletionEpoch", float64(0)))
			case "backingFingerprint":
				_ = d.Set(field, strings.Repeat("f", 64))
			}
			_ = b.Set("readDependencies", []any{d})
			s := &memoryStorage{rows: []*Object{a, b}}
			if e := (&Lifecycle{s}).PurgeAssertion("deleted"); e != nil {
				t.Fatal(e)
			}
			if strings.Join(s.effects, ",") != "replace:a" {
				t.Fatal(s.effects)
			}
		})
	}
}
func TestPurgeTransitiveDeletionAndOrder(t *testing.T) {
	q := testProof("q")
	a, b, c := testEdge("a", q), testEdge("b", q), testEdge("c", q)
	_ = b.Set("readDependencies", []any{readDep(a, q)})
	_ = c.Set("dependencies", []any{obj("entity", obj("id", "en", "sceneId", "s", "kind", "task", "title", "Synthetic", "backing", get(b, "backing"), "originEdgeId", "b"), "backingFingerprint", strings.Repeat("c", 64), "proofs", []any{q})})
	s := &memoryStorage{rows: []*Object{c, b, a}}
	if e := (&Lifecycle{s}).PurgeAssertion("backing-a"); e != nil {
		t.Fatal(e)
	}
	if strings.Join(s.effects, ",") != "remove:b,remove:a,remove:c" {
		t.Fatal(s.effects)
	}
}
func TestMaintenanceValidatesBeforeEffects(t *testing.T) {
	p, q := testProof("p", "deleted"), testProof("q")
	a := testEdge("a", p, q)
	b := testEdge("b", p)
	_ = a.Set("version", float64(9007199254740991))
	s := &memoryStorage{rows: []*Object{a, b}}
	if e := (&Lifecycle{s}).PurgeAssertion("deleted"); e == nil {
		t.Fatal("unsafe increment accepted")
	}
	if len(s.effects) != 0 {
		t.Fatal(s.effects)
	}
}
func TestNoSurvivingSpanAndEmptyIntersection(t *testing.T) {
	p, q := testProof("p", "deleted"), testProof("q")
	a := testEdge("a", p, q)
	_ = a.Set("spans", arr(get(a, "spans"))[:1])
	s := &memoryStorage{rows: []*Object{a}}
	if e := (&Lifecycle{s}).PurgeAssertion("deleted"); e != nil {
		t.Fatal(e)
	}
	if strings.Join(s.effects, ",") != "remove:a" {
		t.Fatal(s.effects)
	}
	a = testEdge("a", p, q)
	b := testEdge("b", q)
	_ = b.Set("readDependencies", []any{readDep(a, testProof("unrelated"))})
	s = &memoryStorage{rows: []*Object{a, b}}
	if e := (&Lifecycle{s}).PurgeAssertion("deleted"); e != nil {
		t.Fatal(e)
	}
	if strings.Join(s.effects, ",") != "remove:b,replace:a" {
		t.Fatal(s.effects)
	}
}
func TestStrictEdgeSchema(t *testing.T) {
	base := testEdge("a", testProof("p"))
	if e := ValidateEdge(base); e != nil {
		t.Fatal(e)
	}
	mutations := map[string]func(*Object){"unknown": func(e *Object) { _ = e.Set("extra", true) }, "root": func(e *Object) { _ = object(arr(get(e, "spans"))[0]).Set("rootId", strings.Repeat("0", 64)) }, "unbound-parent": func(e *Object) {
		p := object(arr(get(e, "proofs"))[0])
		_ = p.Set("parents", []any{"x"})
		_ = p.Set("parentReferences", []any{})
	}, "span-order": func(e *Object) { _ = object(arr(get(e, "spans"))[0]).Set("end", float64(0)) }, "property": func(e *Object) { _ = e.Set("property", "count") }, "read-pair": func(e *Object) {
		d := readDep(base, testProof("p"))
		d.Delete("edgeVersion")
		_ = e.Set("readDependencies", []any{d})
	}, "unknown-event-field": func(e *Object) {
		p := arr(get(e, "proofs"))[0]
		source := object(arr(get(p, "sources"))[0])
		_ = source.Set("eventRef", obj("eventId", "e", "revision", float64(1), "deletionEpoch", float64(0), "extra", true))
	}, "bad-time": func(e *Object) { _ = e.Set("createdAt", "2026-02-30T00:00:00Z") }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			e := object(clone(base))
			mutate(e)
			if ValidateEdge(e) == nil {
				t.Fatal("accepted")
			}
		})
	}
}
