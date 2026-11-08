package experienceservice

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"reflect"
)

// Material retained from the owner's existing row decode. Policy scalar presence
// is shared by persisted JSON and CandidateHash; no global MarshalJSON behavior
// changes. The
// response allowlist is separate from exact existing outcome/receipt material
// retained exclusively for recovery qualification. It never enters HTTP output.
type presentationMaterial struct {
	raw            *jsjson.Object
	recoveryRecord *jsjson.Object
}
type adminPresentation struct {
	policy            *jsjson.Object
	sources           []*jsjson.Object
	validation        any
	validationPresent bool
}

func keepPresentation(o *jsjson.Object, keys ...string) *jsjson.Object {
	out := new(jsjson.Object)
	if o == nil {
		return out
	}
	for _, k := range keys {
		if v, ok := o.Get(k); ok {
			check(out.Set(k, cloneReferenceValue(v)))
		}
	}
	return out
}
func selectedPresentation(kind string, o *jsjson.Object) *jsjson.Object {
	switch kind {
	case "policy":
		out := keepPresentation(o, "id", "version", "action", "condition", "activationBasis", "status", "createdAt", "expiresAt")
		if c, ok := getValue(out, "condition").(*jsjson.Object); ok {
			keys := []string{"kind"}
			if getValue(c, "kind") != "plan_question" {
				keys = append(keys, "personId")
			}
			check(out.Set("condition", keepPresentation(c, keys...)))
		}
		return out
	case "outcome":
		out := keepPresentation(o, "source", "observedAt", "code")
		if source, ok := getValue(out, "source").(*jsjson.Object); ok {
			check(out.Set("source", keepPresentation(source, "messageId", "senderId")))
		}
		return out
	case "receipt":
		return keepPresentation(o, "tier", "checks", "failures", "passed")
	}
	panic("unknown presentation type")
}
func retainPresentation(value any, raw *jsjson.Object) {
	var kind string
	var target **presentationMaterial
	switch v := value.(type) {
	case *Policy:
		kind = "policy"
		target = &v.presentation
	case *Outcome:
		kind = "outcome"
		target = &v.presentation
	case *Receipt:
		kind = "receipt"
		target = &v.presentation
	default:
		return
	}
	*target = &presentationMaterial{raw: selectedPresentation(kind, raw)}
	if kind == "outcome" || kind == "receipt" {
		(*target).recoveryRecord = cloneReferenceValue(raw).(*jsjson.Object)
	}
}

// applyPolicyPresentationPatch follows the owner's explicit source patch sites,
// not equality against zero values. Every returned sidecar is a fresh copy, so
// copying and then mutating a Policy cannot rewrite its previous presentation.
func applyPolicyPresentationPatch(p Policy, keys ...string) Policy {
	if p.presentation == nil {
		return p
	}
	raw := cloneReferenceValue(p.presentation.raw).(*jsjson.Object)
	current := selectedPresentation("policy", domainValue(reflect.ValueOf(p)).(*jsjson.Object))
	for _, key := range keys {
		if v, ok := current.Get(key); ok {
			check(raw.Set(key, cloneReferenceValue(v)))
		} else {
			raw.Delete(key)
		}
	}
	p.presentation = &presentationMaterial{raw: raw}
	return p
}
func currentPresentation(kind string, value any, material *presentationMaterial) *jsjson.Object {
	if material != nil {
		return cloneReferenceValue(material.raw).(*jsjson.Object)
	}
	return selectedPresentation(kind, domainValue(reflect.ValueOf(value)).(*jsjson.Object))
}

// Source save spreads the original policy and patches known fields. These
// three response scalars must not acquire invented defaults across an expiry
// write, or a second GET would disagree with the first. The same date material
// feeds CandidateHash; other fields use the existing encoding. Creation has no
// decoded sidecar and therefore uses the ordinary domain encoding unchanged.
func policyStorageJSON(p Policy) string {
	if p.presentation == nil {
		return encoded(p)
	}
	out := domainValue(reflect.ValueOf(p)).(*jsjson.Object)
	for _, key := range []string{"createdAt", "expiresAt", "activationBasis"} {
		if v, ok := policyScalarMaterial(p, key); ok {
			check(out.Set(key, cloneReferenceValue(v)))
		} else {
			out.Delete(key)
		}
	}
	return take(jsjson.Stringify(out))
}

func captureAdminPresentation(row AdminPolicy) *adminPresentation {
	out := &adminPresentation{policy: currentPresentation("policy", row.Policy, row.Policy.presentation), sources: []*jsjson.Object{}}
	for _, source := range row.Sources {
		out.sources = append(out.sources, currentPresentation("outcome", source, source.presentation))
	}
	if row.Validation != nil {
		out.validationPresent = true
		out.validation = currentPresentation("receipt", *row.Validation, row.Validation.presentation)
	}
	return out
}

// policyScalarMaterial is shared by hashing, persistence and presentation. It
// preserves absent/null/scalar values from a decoded source policy, including
// values that the response schema will later reject. Explicit owner patches
// update this material before it is used; there is no equality-based guessing.
func policyScalarMaterial(p Policy, key string) (any, bool) {
	if p.presentation != nil {
		return p.presentation.raw.Get(key)
	}
	current := selectedPresentation("policy", domainValue(reflect.ValueOf(p)).(*jsjson.Object))
	return current.Get(key)
}
func recoveryRecordMaterial(value any) any {
	var material *presentationMaterial
	switch v := value.(type) {
	case *Outcome:
		if v != nil {
			material = v.presentation
		}
	case *Receipt:
		if v != nil {
			material = v.presentation
		}
	}
	if material == nil || material.recoveryRecord == nil {
		return value
	}
	return json.RawMessage(take(jsjson.Marshal(material.recoveryRecord)))
}
func validActivationMaterial(p Policy) bool {
	value, present := policyScalarMaterial(p, "activationBasis")
	if !present {
		return true
	}
	s, ok := value.(string)
	return ok && member(s, "explicit_preference", "administrator_review")
}

// clonePolicy preserves source presence while isolating all mutable material
// passed to a validation port. JSON-only cloning loses omitempty/null scalars.
func clonePolicy(p Policy) Policy {
	out := *decode[Policy](encoded(p))
	if p.presentation != nil {
		out.presentation = &presentationMaterial{raw: cloneReferenceValue(p.presentation.raw).(*jsjson.Object)}
	}
	return out
}
func rawRecoveryRecord(raw any) any {
	return json.RawMessage(take(jsjson.Marshal(raw)))
}

// receiptRecord is an existing point read, deliberately not a mechanical
// receipt decoder. Recovery hashes and HTTP projection use the stored object.
func (s store) receiptRecord(id string) (any, bool) {
	rows := s.values("SELECT value FROM memory_experience_validations WHERE id=?", id)
	if len(rows) == 0 {
		return nil, false
	}
	return take(jsjson.Parse(rows[0])), true
}
