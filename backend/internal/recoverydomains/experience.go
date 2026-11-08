package recoverydomains

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func sourceOutcomes(ctx context.Context, tx *recovery.MaintenanceTx, scene, msg string) ([]*object, error) {
	return values(ctx, tx, "SELECT value FROM memory_experience_outcomes WHERE scene=? AND (source=? OR json_extract(value,'$.target.messageId')=?)", scene, msg, msg)
}
func policiesForOutcome(ctx context.Context, tx *recovery.MaintenanceTx, scene, id string) ([]*object, error) {
	return values(ctx, tx, "SELECT p.value FROM memory_experience_policies p WHERE p.scene=? AND (EXISTS(SELECT 1 FROM json_each(p.value,'$.supportIds') j WHERE j.value=?) OR EXISTS(SELECT 1 FROM json_each(p.value,'$.counterexampleIds') j WHERE j.value=?))", scene, id, id)
}
func tombstone(ctx context.Context, tx *recovery.MaintenanceTx, k string) error {
	return exec(ctx, tx, "INSERT OR IGNORE INTO memory_experience_tombstones VALUES(?)", k)
}
func eraseOutcome(ctx context.Context, tx *recovery.MaintenanceTx, outcome *object) error {
	if e := tombstone(ctx, tx, str(get(outcome, "rootId"))); e != nil {
		return e
	}
	return exec(ctx, tx, "DELETE FROM memory_experience_outcomes WHERE id=?", get(outcome, "id"))
}
func eraseTrial(ctx context.Context, tx *recovery.MaintenanceTx, id string, options Options) error {
	trials, e := trialRows(ctx, tx)
	if e != nil {
		return e
	}
	for _, r := range trials {
		if r.policy != id || r.receipt == nil {
			continue
		}
		if e = recordTrialMutation(ctx, tx, nil, r.receipt, "erase", options); e != nil {
			return e
		}
		for _, v := range arr(get(r.receipt, "positives")) {
			outcome, e := read(ctx, tx, "SELECT value FROM memory_experience_outcomes WHERE id=?", get(obj(v), "outcomeId"))
			if e != nil {
				return e
			}
			if outcome != nil {
				if e = eraseOutcome(ctx, tx, outcome); e != nil {
					return e
				}
			}
		}
		return writeTrial(ctx, tx, nil, r.receipt, "erase", options)
	}
	return nil
}
func (o *Owner) erasePolicy(ctx context.Context, tx *recovery.MaintenanceTx, p *object) error {
	if e := eraseTrial(ctx, tx, str(get(p, "id")), o.options); e != nil {
		return e
	}
	if e := tx.RecordReplayEffect(ctx, recovery.ExperienceDelete{SceneID: str(get(p, "sceneId")), ID: str(get(p, "id"))}); e != nil {
		return e
	}
	if e := tombstone(ctx, tx, key("policy", str(get(p, "id")))); e != nil {
		return e
	}
	for _, table := range []string{"memory_experience_validations", "memory_experience_uses"} {
		if e := exec(ctx, tx, "DELETE FROM "+table+" WHERE policy=?", get(p, "id")); e != nil {
			return e
		}
	}
	return exec(ctx, tx, "DELETE FROM memory_experience_policies WHERE id=?", get(p, "id"))
}
func (o *Owner) deleteExperience(ctx context.Context, tx *recovery.MaintenanceTx, scene, id string) error {
	p, e := read(ctx, tx, "SELECT value FROM memory_experience_policies WHERE id=?", id)
	if e != nil {
		return e
	}
	if p == nil {
		return tombstone(ctx, tx, key("policy", id))
	}
	if str(get(p, "sceneId")) != scene {
		return errors.New("recovery_experience_scope_mismatch")
	}
	supports, e := requiredArray(p, "supportIds")
	if e != nil {
		return e
	}
	counters, e := requiredArray(p, "counterexampleIds")
	if e != nil {
		return e
	}
	ids := append(append([]any{}, supports...), counters...)
	for _, id := range ids {
		v, e := read(ctx, tx, "SELECT value FROM memory_experience_outcomes WHERE id=?", id)
		if e != nil {
			return e
		}
		if v != nil {
			if e = eraseOutcome(ctx, tx, v); e != nil {
				return e
			}
		}
	}
	return o.erasePolicy(ctx, tx, p)
}
func (o *Owner) revokeExperienceSource(ctx context.Context, tx *recovery.MaintenanceTx, scene, msg string) error {
	if e := tombstone(ctx, tx, key("source", scene, msg)); e != nil {
		return e
	}
	outcomes, e := sourceOutcomes(ctx, tx, scene, msg)
	if e != nil {
		return e
	}
	for _, outcome := range outcomes {
		trials, e := trialRows(ctx, tx)
		if e != nil {
			return e
		}
		for _, r := range trials {
			if r.scene != scene || r.receipt == nil {
				continue
			}
			for _, v := range arr(get(r.receipt, "positives")) {
				if get(obj(v), "outcomeId") == get(outcome, "id") {
					if e = eraseTrial(ctx, tx, r.policy, o.options); e != nil {
						return e
					}
					break
				}
			}
		}
		policies, e := policiesForOutcome(ctx, tx, scene, str(get(outcome, "id")))
		if e != nil {
			return e
		}
		for _, p := range policies {
			if e = o.erasePolicy(ctx, tx, p); e != nil {
				return e
			}
		}
		if e = eraseOutcome(ctx, tx, outcome); e != nil {
			return e
		}
	}
	return nil
}
func (o *Owner) experienceQualification(ctx context.Context, tx *recovery.MaintenanceTx, p *object) (string, error) {
	v := makeObject("id", get(p, "id"), "sceneId", get(p, "sceneId"), "hash", get(p, "hash"))
	for _, pair := range [][2]string{{"supportIds", "supports"}, {"counterexampleIds", "counterexamples"}} {
		items := []any{}
		ids, e := requiredArray(p, pair[0])
		if e != nil {
			return "", e
		}
		for _, id := range ids {
			outcome, e := read(ctx, tx, "SELECT value FROM memory_experience_outcomes WHERE id=?", id)
			if e != nil {
				return "", e
			}
			if outcome == nil {
				items = append(items, nil)
			} else {
				items = append(items, outcome)
			}
		}
		set(v, pair[1], items)
	}
	if id := str(get(p, "validationId")); id != "" {
		receipt, e := read(ctx, tx, "SELECT value FROM memory_experience_validations WHERE id=?", id)
		if e != nil {
			return "", e
		}
		if receipt != nil {
			set(v, "validation", receipt)
		} // absent receipt is undefined, omitted in the source object.
	} else {
		set(v, "validation", nil)
	}
	return hash(v, o.options)
}
func (o *Owner) supported(ctx context.Context, tx *recovery.MaintenanceTx, p *object) (bool, error) {
	ids := arr(get(p, "supportIds"))
	if len(ids) == 0 {
		return false, nil
	}
	for _, id := range ids {
		outcome, e := read(ctx, tx, "SELECT value FROM memory_experience_outcomes WHERE id=?", id)
		if e != nil {
			return false, e
		}
		scene := str(get(p, "sceneId"))
		if outcome == nil || str(get(outcome, "sceneId")) != scene {
			return false, nil
		}
		source, target := obj(get(outcome, "source")), obj(get(outcome, "target"))
		if target != nil {
			target = target.Clone()
			target.Delete("senderId")
		}
		denied, e := exists(ctx, tx, "SELECT 1 FROM memory_experience_tombstones WHERE key=?", key("source", scene, str(get(source, "messageId"))))
		if e != nil || denied {
			return false, e
		}
		for _, s := range []*object{source, target} {
			ok, e := sourceCurrent(ctx, tx, scene, s, yes(get(outcome, "snapshotBound")), false)
			if e != nil || !ok {
				return false, e
			}
		}
	}
	return true, nil
}
func (o *Owner) stateExperience(ctx context.Context, tx *recovery.MaintenanceTx, event *object) error {
	p, e := read(ctx, tx, "SELECT value FROM memory_experience_policies WHERE id=?", get(event, "id"))
	if e != nil {
		return e
	}
	if p == nil || get(p, "sceneId") != get(event, "sceneId") || num(get(p, "version")) > num(get(event, "version")) {
		return errors.New("recovery_experience_qualification_missing")
	}
	previous := get(p, "version")
	next := p.Clone()
	for _, k := range []string{"status", "version", "validationId", "activationBasis", "reason"} {
		if v, ok := event.Get(k); ok {
			set(next, k, v)
		} else {
			next.Delete(k)
		}
	}
	h, e := o.experienceQualification(ctx, tx, next)
	if e != nil {
		return e
	}
	if h != str(get(event, "qualificationHash")) {
		return errors.New("recovery_experience_qualification_missing")
	}
	if str(get(next, "status")) == "active" {
		supported, e := o.supported(ctx, tx, next)
		if e != nil {
			return e
		}
		receipt, e := read(ctx, tx, "SELECT value FROM memory_experience_validations WHERE id=?", get(next, "validationId"))
		if e != nil {
			return e
		}
		if !supported || str(get(next, "validationId")) == "" || receipt == nil {
			return errors.New("recovery_experience_grant_invalid")
		}
	}
	raw, e := encode(next)
	if e != nil {
		return e
	}
	r, e := tx.ExecContext(ctx, "UPDATE memory_experience_policies SET version=?,value=? WHERE id=? AND version=?", get(next, "version"), raw, get(next, "id"), previous)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("experience_version_conflict")
	}
	return nil
}
