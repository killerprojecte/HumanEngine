package experienceservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
)

// Validate after serialization so integer representations share JavaScript's
// number domain. Fields are constructed by allowlist before this check.
func validateHTTPView(value *jsjson.Object) error {
	return guarded(func() {
		o := take(jsjson.Parse(string(take(jsjson.Marshal(value))))).(*jsjson.Object)
		invalid := func() { check(errors.New("experience_http_view_invalid")) }
		str := func(o *jsjson.Object, k string) string {
			v, ok := getValue(o, k).(string)
			if !ok {
				invalid()
			}
			return v
		}
		id := func(o *jsjson.Object, k string) {
			v := str(o, k)
			n := take(jsjson.CodePointCount(v))
			if n < 1 || n > 2048 {
				invalid()
			}
		}
		number := func(o *jsjson.Object, k string, min, max float64) {
			v, ok := getValue(o, k).(float64)
			if !ok || math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < min || v > max {
				invalid()
			}
		}
		enum := func(o *jsjson.Object, k string, values ...string) {
			if !member(str(o, k), values...) {
				invalid()
			}
		}
		obj := func(o *jsjson.Object, k string) *jsjson.Object {
			v, ok := getValue(o, k).(*jsjson.Object)
			if !ok {
				invalid()
			}
			return v
		}
		boolean := func(o *jsjson.Object, k string) {
			if _, ok := getValue(o, k).(bool); !ok {
				invalid()
			}
		}
		optionalEnum := func(o *jsjson.Object, k string, values ...string) {
			if _, yes := o.Get(k); yes {
				enum(o, k, values...)
			}
		}
		id(o, "id")
		number(o, "version", 1, 9007199254740991)
		enum(o, "action", "exact_person_before_semantic", "verify_plan_qualifiers", "conclusion_before_detail")
		c := obj(o, "condition")
		enum(c, "kind", "person_question", "plan_question", "response_style")
		if getValue(c, "kind") != "plan_question" {
			id(c, "personId")
		}
		boolean(o, "trialEligible")
		boolean(o, "expired")
		str(o, "createdAt")
		str(o, "expiresAt")
		enum(o, "status", "candidate", "validated", "active", "suspended", "retired")
		optionalEnum(o, "activationBasis", "explicit_preference", "administrator_review")
		if _, ok := o.Get("trial"); ok {
			t := obj(o, "trial")
			for _, k := range []string{"version", "policyVersion", "evidencePolicyVersion"} {
				number(t, k, 1, 9007199254740991)
			}
			number(t, "positiveCount", 0, 2)
			enum(t, "phase", "trial", "promoted", "suspended", "expired")
			str(t, "createdAt")
			str(t, "expiresAt")
			i := obj(t, "instruction")
			id(i, "id")
			number(i, "version", 1, 9007199254740991)
			optionalEnum(t, "reason", "explicit_instruction_trial", "user_reported_helpfulness", "negative_feedback", "instruction_withdrawn", "expiry", "qualification_changed")
		}
		sources, ok := getValue(o, "sources").([]any)
		if !ok {
			invalid()
		}
		for _, v := range sources {
			source, ok := v.(*jsjson.Object)
			if !ok {
				invalid()
			}
			id(source, "messageId")
			id(source, "senderId")
			str(source, "observedAt")
			enum(source, "code", "request_exact_person_lookup", "request_verify_plan_qualifiers", "request_conclusion_first", "counterexample", "withdraw_instruction", "withdraw_person_instruction", "withdraw_plan_instruction", "user_reported_helpful")
		}
		if _, ok := o.Get("validation"); ok {
			v := obj(o, "validation")
			enum(v, "tier", "mechanical_only")
			boolean(v, "passed")
			for _, k := range []string{"checks", "failures"} {
				a, ok := getValue(v, k).([]any)
				if !ok {
					invalid()
				}
				for _, x := range a {
					if _, ok := x.(string); !ok {
						invalid()
					}
				}
			}
		}
		u := obj(o, "uses")
		for _, k := range []string{"exposed", "applied", "declined"} {
			number(u, k, 0, 9007199254740991)
		}
	})
}
