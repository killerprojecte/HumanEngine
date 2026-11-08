package semantic

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

type purgeEffect struct{ before, after *jsjson.Object }

// purgeCandidate plans one effect without changing input objects. Loss of any
// direct parent or all of any parent's proofs deletes the complete candidate.
func purgeCandidate(before *jsjson.Object, id string) (*jsjson.Object, bool, error) {
	after := before.Clone()
	changed := false
	for _, field := range []string{"parents", "anchorDependencies"} {
		values := semGet(before, field).([]any)
		mapped := make([]any, 0, len(values))
		for _, v := range values {
			parent := v.(*jsjson.Object)
			ref := semGet(parent, "ref").(*jsjson.Object)
			if semString(ref, "id") == id {
				return nil, true, nil
			}
			proofs := semGet(parent, "proofs").([]any)
			kept := make([]any, 0, len(proofs))
			for _, v := range proofs {
				proof := v.(*jsjson.Object)
				drop := false
				for _, p := range semGet(proof, "parents").([]any) {
					if p == id {
						drop = true
						break
					}
				}
				if !drop {
					kept = append(kept, proof)
				}
			}
			if len(kept) == 0 {
				return nil, true, nil
			}
			if len(kept) != len(proofs) {
				changed = true
				parent = parent.Clone()
				_ = parent.Set("proofs", kept)
			}
			mapped = append(mapped, parent)
		}
		_ = after.Set(field, mapped)
	}
	if !changed {
		return before, false, nil
	}
	_ = after.Set("version", semNumber(before, "version")+1)
	after, e := ValidateCandidate(after)
	return after, true, e
}

// PurgeAssertion is a trusted privacy maintenance operation. It deliberately
// needs no current/partition/episode-anchor resolver. Attempts are processed in
// input-key order first; candidate effects are all collected and validated in
// id order before any candidate write, matching SemanticLifecycle.transform.
func (s *Store) PurgeAssertion(id string) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	attempts, e := s.Attempts()
	if e != nil {
		return e
	}
	for _, attempt := range attempts {
		contains := false
		for _, v := range semGet(attempt, "parents").([]any) {
			if semString(semGet(v.(*jsjson.Object), "ref").(*jsjson.Object), "id") == id {
				contains = true
				break
			}
		}
		if !contains {
			continue
		}
		next := attempt.Clone()
		_ = next.Set("parents", []any{})
		_ = next.Set("roots", []any{})
		_ = next.Set("status", "abandoned")
		_ = next.Set("version", semNumber(attempt, "version")+1)
		if e = s.WriteAttempt(next, attempt); e != nil {
			return e
		}
	}
	candidates, e := s.Rows(nil)
	if e != nil {
		return e
	}
	effects := []purgeEffect{}
	for _, before := range candidates {
		after, changed, e := purgeCandidate(before, id)
		if e != nil {
			return e
		}
		if changed {
			effects = append(effects, purgeEffect{before, after})
		}
	}
	for _, effect := range effects {
		if e = s.Write(effect.after, effect.before, "privacy"); e != nil {
			return e
		}
	}
	return nil
}
