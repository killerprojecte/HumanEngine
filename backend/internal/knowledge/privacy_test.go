package knowledge

import (
	"errors"
	"reflect"
	"testing"
)

type privacyMemoryStorage struct{ memoryStorage }

func (s *privacyMemoryStorage) Rows(scene *string, limit *int) ([]*Object, error) {
	if scene == nil || *scene != "s" || limit != nil {
		return nil, errors.New("unexpected privacy scope")
	}
	return s.rows, nil
}
func TestPrivacyPruneUnknownErrorPrecedesEveryWrite(t *testing.T) {
	p, q := testProof("p"), testProof("q")
	a, b := testEdge("a", p), testEdge("b", q)
	st := &privacyMemoryStorage{memoryStorage{rows: []*Object{a, b}}}
	l := &Lifecycle{storage: st}
	failure := errors.New("unknown_parent_traversal")
	calls := []string{}
	e := l.PruneUnavailableProofChecked("s", false, func(ref, proof *Object) (bool, error) {
		calls = append(calls, str(get(ref, "id")))
		if get(proof, "proofId") == "q" {
			return false, failure
		}
		return false, nil
	})
	if !errors.Is(e, failure) || !reflect.DeepEqual(calls, []string{"backing-a", "backing-b"}) || len(st.effects) != 0 {
		t.Fatalf("error=%v calls=%v effects=%v", e, calls, st.effects)
	}
}
func TestPrivacyRequalifyRequiresExactCapturedReference(t *testing.T) {
	p := testProof("p")
	edge := testEdge("a", p)
	st := &privacyMemoryStorage{memoryStorage{rows: []*Object{edge}}}
	l := &Lifecycle{storage: st}
	calls := 0
	current := func(Context, Reference) (*Assertion, error) { calls++; return nil, errors.New("must_not_query_latest") }
	old := obj("id", "backing-a", "version", float64(2), "deletionEpoch", float64(0))
	next := obj("id", "backing-a", "version", float64(3), "deletionEpoch", float64(1))
	if e := l.Requalify(Context{SceneID: "s"}, []Requalification{{old, next}}, current); e != nil || calls != 0 || len(st.effects) != 0 {
		t.Fatalf("e=%v calls=%d effects=%v", e, calls, st.effects)
	}
	old = object(get(edge, "backing"))
	next = next.Clone()
	_ = next.Set("id", "other")
	e := l.Requalify(Context{SceneID: "s"}, []Requalification{{old, next}}, current)
	if e == nil || e.Error() != "knowledge_requalification_invalid" || calls != 0 {
		t.Fatalf("invalid transition %v calls=%d", e, calls)
	}
}
