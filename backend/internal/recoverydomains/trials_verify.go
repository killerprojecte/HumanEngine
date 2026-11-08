package recoverydomains

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func verifyTrialMaterial(ctx context.Context, tx *recovery.MaintenanceTx, rows []trialRow) error {
	bad := errors.New("recovery_experience_trial_evidence_missing")
	for _, row := range rows {
		if row.receipt != nil {
			if e := exec(ctx, tx, experienceDDL); e != nil {
				return e
			}
			break
		}
	}
	for _, row := range rows {
		t := row.receipt
		if t == nil {
			continue
		}
		policy, e := read(ctx, tx, "SELECT value FROM memory_experience_policies WHERE id=?", get(t, "policyId"))
		if e != nil {
			return e
		}
		instruction, e := read(ctx, tx, "SELECT value FROM memory_experience_outcomes WHERE id=?", get(obj(get(t, "instruction")), "id"))
		if e != nil {
			return e
		}
		validation, e := read(ctx, tx, "SELECT value FROM memory_experience_validations WHERE id=?", get(t, "validationId"))
		if e != nil {
			return e
		}
		active := str(get(t, "phase")) == "trial" || str(get(t, "phase")) == "promoted"
		code := "request_verify_plan_qualifiers"
		if str(get(policy, "action")) == "exact_person_before_semantic" {
			code = "request_exact_person_lookup"
		}
		if policy == nil || get(policy, "sceneId") != get(t, "sceneId") || num(get(policy, "version")) < num(get(t, "policyVersion")) || active && (get(policy, "version") != get(t, "policyVersion") || get(policy, "hash") != get(t, "policyHash") || str(get(policy, "status")) != "validated") || instruction == nil || get(instruction, "version") != get(obj(get(t, "instruction")), "version") || get(instruction, "sceneId") != get(t, "sceneId") || get(obj(get(instruction, "source")), "senderId") != get(t, "requesterId") || !yes(get(instruction, "snapshotBound")) || str(get(instruction, "code")) != code || !contains(arr(get(policy, "supportIds")), get(instruction, "id")) || validation == nil || !yes(get(validation, "passed")) || str(get(validation, "tier")) != "mechanical_only" || get(validation, "candidateId") != get(policy, "id") || get(validation, "candidateHash") != get(t, "policyHash") || num(get(validation, "candidateVersion")) != num(get(t, "evidencePolicyVersion"))-1 || str(get(validation, "ruleVersion")) != "experience-mechanical-v2" || str(get(validation, "datasetVersion")) != "experience-fixed-behavior-holdout-v2" {
			return bad
		}
		scene := str(get(t, "sceneId"))
		for _, k := range []string{"source", "target"} {
			rev, e := recalled(ctx, tx, scene, str(get(obj(get(instruction, k)), "messageId")))
			if e != nil {
				return e
			}
			if rev {
				return bad
			}
		}
		operation := ""
		switch str(get(policy, "action")) {
		case "exact_person_before_semantic":
			operation = "memory_person"
		case "verify_plan_qualifiers":
			operation = "plan_qualifiers_checked"
		}
		if operation == "" {
			return bad
		}
		for _, v := range arr(get(t, "positives")) {
			item := obj(v)
			outcome, e := read(ctx, tx, "SELECT value FROM memory_experience_outcomes WHERE id=?", get(item, "outcomeId"))
			if e != nil {
				return e
			}
			source, target := obj(get(outcome, "source")), obj(get(outcome, "target"))
			ref := obj(get(outcome, "policyRef"))
			if outcome == nil || get(outcome, "version") != get(item, "outcomeVersion") || get(outcome, "sceneId") != get(t, "sceneId") || str(get(outcome, "code")) != "user_reported_helpful" || !yes(get(outcome, "snapshotBound")) || get(source, "senderId") != get(t, "requesterId") || get(ref, "id") != get(t, "policyId") || get(ref, "version") != get(t, "evidencePolicyVersion") || key("source", scene, str(get(source, "messageId"))) != str(get(item, "sourceRoot")) || key("source", scene, str(get(target, "messageId"))) != str(get(item, "targetRoot")) {
				return bad
			}
			for _, s := range []*object{source, target} {
				rev, e := recalled(ctx, tx, scene, str(get(s, "messageId")))
				if e != nil {
					return e
				}
				if rev {
					return bad
				}
			}
			uses, e := values(ctx, tx, "SELECT value FROM memory_experience_uses WHERE scene=? AND target=?", scene, get(target, "messageId"))
			if e != nil {
				return e
			}
			found := false
			for _, u := range uses {
				ref := obj(get(u, "policyRef"))
				if str(get(u, "kind")) == "applied" && str(get(u, "operation")) == operation && get(ref, "id") == get(t, "policyId") && get(ref, "version") == get(t, "evidencePolicyVersion") && get(u, "turnId") == get(item, "turnId") {
					found = true
					break
				}
			}
			if !found {
				return bad
			}
		}
	}
	return nil
}
func (t *Trials) Verify(ctx context.Context, tx *recovery.MaintenanceTx, events []string) error {
	if e := tx.CheckBinding(ctx, t.owner); e != nil {
		return e
	}
	rows, e := trialRows(ctx, tx)
	if e != nil {
		return e
	}
	if e = verifyTrialMaterial(ctx, tx, rows); e != nil {
		return e
	}
	latest, e := latestTrials(events)
	if e != nil {
		return e
	}
	for _, v := range latest {
		row := matchingTrial(rows, v)
		if row == nil {
			return errors.New("recovery_experience_trial_material_missing")
		}
		done, e := trialAtHead(row, v, t.options)
		if e != nil {
			return e
		}
		if !done {
			if get(v, "afterHash") == nil {
				return errors.New("recovery_experience_trial_delete_incomplete")
			}
			return errors.New("recovery_experience_trial_head_mismatch")
		}
	}
	return nil
}
