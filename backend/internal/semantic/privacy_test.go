package semantic

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"reflect"
	"testing"
)

func TestPrivacyPruneVisitsAllParentsBeforeWrites(t *testing.T) {
	f := semSetup(t)
	candidate := semCandidate(t, "one", "a")
	semMust(t, f.run(func(s *Store) error { return s.Write(candidate, nil, "admit") }))
	before, e := f.authority.Head(context.Background())
	semMust(t, e)
	calls := []string{}
	failure := errors.New("unknown_parent_traversal")
	e = f.run(func(s *Store) error {
		return s.PruneUnavailableProof(Context{SceneID: "scene"}, func(ref, proof *jsjson.Object) (bool, error) {
			id := scStr(scGet(ref, "id"))
			calls = append(calls, id)
			if id == "b" {
				return false, failure
			}
			return false, nil
		})
	})
	if !errors.Is(e, failure) || !reflect.DeepEqual(calls, []string{"a", "b"}) {
		t.Fatalf("e=%v calls=%v", e, calls)
	}
	after, e := f.authority.Head(context.Background())
	semMust(t, e)
	if before != after {
		t.Fatal("planned partial transform leaked intent")
	}
	var got *jsjson.Object
	semMust(t, f.run(func(s *Store) error {
		var e error
		got, e = s.Get("one")
		return e
	}))
	if !privacyEqual(got, candidate) {
		t.Fatal("failed prune changed row")
	}
}
func TestPrivacySimulationChecksTransactionBeforeShortCircuit(t *testing.T) {
	f := semSetup(t)
	if e := f.store.Requalify(Context{Simulation: true}, nil, nil, PrivacyAuthority{}); e == nil {
		t.Fatal("simulation bypassed owner")
	}
	semMust(t, f.run(func(s *Store) error { return s.Requalify(Context{Simulation: true}, nil, nil, PrivacyAuthority{}) }))
}
func TestPrivacyCapturedDefinitionIncludesStatusAndAnchorQualifications(t *testing.T) {
	original := semCandidate(t, "one", "a")
	a, e := DefinitionHash(original)
	semMust(t, e)
	next := original.Clone()
	_ = next.Set("status", "rejected")
	b, e := DefinitionHash(next)
	semMust(t, e)
	if a == b {
		t.Fatal("status missing from definition identity")
	}
	next = original.Clone()
	_ = next.Set("version", float64(8))
	b, e = DefinitionHash(next)
	semMust(t, e)
	if a != b {
		t.Fatal("privacy version must not change definition identity")
	}
}
