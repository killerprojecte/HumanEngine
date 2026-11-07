package recovery

import (
	"context"
	"math"
	"strings"
	"testing"
)

// Construct intents directly rather than relying on the oracle-to-struct decoder.
func TestRemainingReviewNilListsAndNonTailDedup(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	identity := IdentityMutation{Operation: "alias_remove", OperationID: "operation", ExpectedRevision: math.Copysign(0, -1), NextRevision: 1, BeforeHash: strings.Repeat("a", 64), QualificationHash: strings.Repeat("b", 64), Targets: IdentityTargets{AliasIDs: []string{"alias"}}}
	first, err := f.authority.Append(ctx, identity, f.gate.Head, false)
	if err != nil {
		t.Fatal(err)
	}
	var original string
	if err = f.external.QueryRow("SELECT payload FROM revocations WHERE sequence=1").Scan(&original); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(original, `"expectedRevision":0`) || !strings.Contains(original, `"endpoints":[]`) {
		t.Fatal(original)
	}
	tail, err := f.authority.Append(ctx, CommitmentDelete{SceneID: "scene", ID: "later", DeletionEpoch: 0}, first, false)
	if err != nil {
		t.Fatal(err)
	}
	identity.ExpectedRevision = 0
	identity.Targets.Endpoints = []IdentityEndpoint{}
	got, err := f.authority.Append(ctx, identity, tail, false)
	if err != nil || got != tail {
		t.Fatalf("non-tail match returned %+v, want current %+v: %v", got, tail, err)
	}
	if _, err = f.authority.Append(ctx, identity, first, false); err == nil {
		t.Fatal("historical match bypassed expected-head comparison")
	}
	forced, err := f.authority.Append(ctx, identity, tail, true)
	if err != nil || forced.Sequence != tail.Sequence+1 {
		t.Fatal(forced, err)
	}
}

func TestRemainingReviewCallerMutationCannotRewriteHistory(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	version, hash := float64(1), strings.Repeat("a", 64)
	state := &SemanticNextState{Status: "admitted", Admission: "administrator_review"}
	intent := SemanticMutation{Operation: "admit", ID: "\xed\xa0\x80", SceneID: "scene", NextVersion: &version, AfterHash: &hash, NextState: state}
	first, err := f.authority.Append(ctx, intent, f.gate.Head, false)
	if err != nil {
		t.Fatal(err)
	}
	var original string
	if err = f.external.QueryRow("SELECT payload FROM revocations WHERE sequence=1").Scan(&original); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(original, `"id":"\ud800"`) || !strings.Contains(original, `"previousVersion":null`) || !strings.Contains(original, `"beforeHash":null`) {
		t.Fatal(original)
	}
	version = 2
	hash = strings.Repeat("b", 64)
	state.Status = "rejected"
	intent.Operation = "review"
	previous, before := float64(1), strings.Repeat("a", 64)
	intent.PreviousVersion, intent.BeforeHash = &previous, &before
	second, err := f.authority.Append(ctx, intent, first, false)
	if err != nil || second.Sequence != first.Sequence+1 {
		t.Fatal(second, err)
	}
	var retained string
	if err = f.external.QueryRow("SELECT payload FROM revocations WHERE sequence=1").Scan(&retained); err != nil || retained != original {
		t.Fatal("caller mutation changed historical bytes", err)
	}
	if head, err := f.authority.Head(ctx); err != nil || head != second {
		t.Fatal(head, err)
	}
}
