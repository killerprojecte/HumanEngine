package experienceservice

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"testing"
)

func TestPresentationCopyAndExplicitPatchIntent(t *testing.T) {
	original := decode[Policy](`{"id":"p","version":1,"action":"verify_plan_qualifiers","condition":{"kind":"plan_question"},"status":"active","expiresAt":"1970-01-02T00:00:00.000Z","activationBasis":""}`)
	if _, ok := original.presentation.raw.Get("createdAt"); ok {
		t.Fatal("missing field invented")
	}
	changed := *original
	changed.Version = 2
	changed.Status = "suspended"
	changed = applyPolicyPresentationPatch(changed, "version", "status")
	if getValue(original.presentation.raw, "status") != "active" || getValue(changed.presentation.raw, "status") != "suspended" {
		t.Fatal("aliased or stale raw status")
	}
	stored := take(jsjson.Parse(policyStorageJSON(changed))).(*jsjson.Object)
	if _, ok := stored.Get("createdAt"); ok {
		t.Fatal("persisted absent date")
	}
	if v, ok := stored.Get("activationBasis"); !ok || v != "" {
		t.Fatal("lost explicit empty activation basis")
	}
	// Explicitly assigning the previous default still creates a field. Equality
	// is not a substitute for the source patch's mutation intent.
	changed.CreatedAt = ""
	changed = applyPolicyPresentationPatch(changed, "createdAt")
	if v, ok := changed.presentation.raw.Get("createdAt"); !ok || v != "" {
		t.Fatal("explicit zero-value patch treated as unchanged")
	}
	if _, ok := original.presentation.raw.Get("createdAt"); ok {
		t.Fatal("patch mutated original sidecar")
	}
	stored = take(jsjson.Parse(policyStorageJSON(changed))).(*jsjson.Object)
	if v, ok := stored.Get("createdAt"); !ok || v != "" {
		t.Fatal("explicit empty date not persisted")
	}
}
func TestPresentationReceiptPresenceWithoutDomainSerializationChange(t *testing.T) {
	receipt := decode[Receipt](`{"tier":"mechanical_only","checks":null,"failures":[],"private":"not-a-response-field"}`)
	view := currentPresentation("receipt", *receipt, receipt.presentation)
	if _, ok := view.Get("passed"); ok {
		t.Fatal("missing passed became false")
	}
	if v, ok := view.Get("checks"); !ok || v != nil {
		t.Fatal("null checks became empty array")
	}
	if _, ok := view.Get("private"); ok {
		t.Fatal("private receipt field leaked")
	}
	if getValue(take(jsjson.Parse(encoded(*receipt))).(*jsjson.Object), "private") != "not-a-response-field" {
		t.Fatal("presentation changed owner serialization")
	}
}

func TestPresentationRecoveryMaterialRetainsAbsentReceiptFields(t *testing.T) {
	raw := `{"candidateId":"p","candidateVersion":1,"candidateHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tier":"mechanical_only","checks":["synthetic"],"failures":[],"passed":true,"secret":"private-receipt"}`
	receipt := decode[Receipt](raw)
	if got := encoded(recoveryRecordMaterial(receipt)); got != raw {
		t.Fatalf("recovery qualification invented fields: %s", got)
	}
	if got := encoded(receipt); got == raw {
		t.Fatal("fixture no longer demonstrates typed serialization difference")
	}
	view := currentPresentation("receipt", *receipt, receipt.presentation)
	if _, ok := view.Get("secret"); ok {
		t.Fatal("recovery-only material leaked into view")
	}
}

func TestPresentationValidationClonePreservesMaterial(t *testing.T) {
	for _, raw := range []string{
		`{"id":"p","createdAt":2099,"expiresAt":"2099-01-31T00:00:00.000Z","activationBasis":""}`,
		`{"id":"p","createdAt":null,"activationBasis":null}`,
		`{"id":"p","createdAt":false,"activationBasis":false}`,
	} {
		original := decode[Policy](raw)
		clone := clonePolicy(*original)
		if CandidateHash(clone) != CandidateHash(*original) {
			t.Fatal("validation clone changed source hash material")
		}
		if validActivationMaterial(clone) {
			t.Fatal("validation clone lost invalid explicit activation basis")
		}
		check(clone.presentation.raw.Set("activationBasis", "administrator_review"))
		if validActivationMaterial(*original) {
			t.Fatal("validation clone aliases original material")
		}
	}
}
