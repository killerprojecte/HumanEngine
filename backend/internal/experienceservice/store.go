// Package experienceservice owns live feedback policies, trials and use evidence.
// Every operation joins one recovery writer; no TS process is involved.
package experienceservice

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// fault is an internal synchronous unwind, converted to error inside the owned
// callback (before COMMIT). It never catches arbitrary application panics.
type fault struct{ err error }

func check(e error) {
	if e != nil {
		panic(fault{e})
	}
}
func fail(code string)           { check(errors.New(code)) }
func take[T any](v T, e error) T { check(e); return v }
func guarded(fn func()) (err error) {
	defer func() {
		if v := recover(); v != nil {
			if f, ok := v.(fault); ok {
				err = f.err
			} else {
				panic(v)
			}
		}
	}()
	fn()
	return
}
func encoded(v any) string        { return string(take(json.Marshal(v))) }
func decode[T any](raw string) *T { var v T; check(json.Unmarshal([]byte(raw), &v)); return &v }

type store struct{ session *recovery.WriteSession }

func (s store) exec(q string, a ...any) sql.Result {
	check(s.session.AssertReady())
	return take(s.session.Exec(q, sqlArgs(a)...))
}
func (s store) values(q string, a ...any) []string {
	check(s.session.AssertReady())
	rows := take(s.session.Query(q, sqlArgs(a)...))
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var raw string
		check(rows.Scan(&raw))
		out = append(out, raw)
	}
	check(rows.Err())
	check(rows.Close())
	return out
}
func one[T any](s store, q string, a ...any) *T {
	rows := s.values(q, a...)
	if len(rows) == 0 {
		return nil
	}
	return decode[T](rows[0])
}
func many[T any](s store, q string, a ...any) []T {
	out := []T{}
	for _, raw := range s.values(q, a...) {
		out = append(out, *decode[T](raw))
	}
	return out
}
func (s store) exists(q string, a ...any) bool {
	check(s.session.AssertReady())
	r := take(s.session.Query(q, sqlArgs(a)...))
	defer r.Close()
	yes := r.Next()
	check(r.Err())
	check(r.Close())
	return yes
}
func (s store) outcome(id string) *Outcome {
	return one[Outcome](s, "SELECT value FROM memory_experience_outcomes WHERE id=?", id)
}
func (s store) root(root string) *Outcome {
	return one[Outcome](s, "SELECT value FROM memory_experience_outcomes WHERE root=?", root)
}
func (s store) policy(id string) *Policy {
	return one[Policy](s, "SELECT value FROM memory_experience_policies WHERE id=?", id)
}
func (s store) receipt(id string) *Receipt {
	return one[Receipt](s, "SELECT value FROM memory_experience_validations WHERE id=?", id)
}
func (s store) tombstoned(k string) bool {
	return s.exists("SELECT 1 FROM memory_experience_tombstones WHERE key=?", k)
}
func (s store) tombstone(k string) {
	s.exec("INSERT OR IGNORE INTO memory_experience_tombstones VALUES(?)", k)
}
func (s store) putOutcome(o Outcome) {
	s.exec("INSERT INTO memory_experience_outcomes VALUES(?,?,?,?,?)", o.ID, o.RootID, o.SceneID, o.Source.MessageID, encoded(o))
}
func (s store) putPolicy(p Policy, expected *int64) {
	if expected == nil {
		s.exec("INSERT INTO memory_experience_policies VALUES(?,?,?,?)", p.ID, p.SceneID, p.Version, policyStorageJSON(p))
		return
	}
	r := s.exec("UPDATE memory_experience_policies SET version=?,value=? WHERE id=? AND version=?", p.Version, policyStorageJSON(p), p.ID, *expected)
	if take(r.RowsAffected()) != 1 {
		fail("experience_version_conflict")
	}
}
func (s store) putReceipt(id string, r Receipt) {
	s.exec("INSERT INTO memory_experience_validations VALUES(?,?,?)", id, r.CandidateID, encoded(r))
}
func (s store) policies(scene string) []Policy {
	return many[Policy](s, "SELECT value FROM memory_experience_policies WHERE scene=? ORDER BY id LIMIT ?", scene, 201)
}
func (s store) outcomes(scene string, limit int) []Outcome {
	return many[Outcome](s, "SELECT value FROM memory_experience_outcomes WHERE scene=? ORDER BY id LIMIT ?", scene, limit)
}
func (s store) policiesForOutcome(scene, id string) []Policy {
	return many[Policy](s, "SELECT p.value FROM memory_experience_policies p WHERE p.scene=? AND (EXISTS(SELECT 1 FROM json_each(p.value,'$.supportIds') j WHERE j.value=?) OR EXISTS(SELECT 1 FROM json_each(p.value,'$.counterexampleIds') j WHERE j.value=?))", scene, id, id)
}
func (s store) sourceOutcomes(scene, id string) []Outcome {
	return many[Outcome](s, "SELECT value FROM memory_experience_outcomes WHERE scene=? AND (source=? OR json_extract(value,'$.target.messageId')=?)", scene, id, id)
}
func (s store) uses(scene, target string) []Use {
	return many[Use](s, "SELECT value FROM memory_experience_uses WHERE scene=? AND target=?", scene, target)
}
func (s store) putUse(root, scene string, u Use) {
	var target any
	if u.TargetMessageID != "" {
		target = u.TargetMessageID
	}
	s.exec("INSERT OR IGNORE INTO memory_experience_uses VALUES(?,?,?,?,?)", root, scene, u.PolicyRef.ID, target, encoded(u))
}
func (s store) useCounts(scene, id string) map[string]int64 {
	counts := map[string]int64{"exposed": 0, "applied": 0, "declined": 0}
	r := take(s.session.Query(`SELECT kind,count(*) FROM (SELECT DISTINCT json_extract(value,'$.turnId') AS turn_id,json_extract(value,'$.policyRef.id') AS policy_id,json_extract(value,'$.policyRef.version') AS policy_version,json_extract(value,'$.kind') AS kind FROM memory_experience_uses WHERE scene=? AND policy=?) GROUP BY kind`, scene, id))
	defer r.Close()
	for r.Next() {
		var k string
		var n int64
		check(r.Scan(&k, &n))
		if _, ok := counts[k]; ok {
			counts[k] = n
		}
	}
	check(r.Err())
	check(r.Close())
	return counts
}
func (s store) eraseOutcome(id string) {
	s.exec("DELETE FROM memory_experience_outcomes WHERE id=?", id)
}
func (s store) erasePolicy(id string) {
	s.exec("DELETE FROM memory_experience_validations WHERE policy=?", id)
	s.exec("DELETE FROM memory_experience_uses WHERE policy=?", id)
	s.exec("DELETE FROM memory_experience_policies WHERE id=?", id)
}

func sqlArgs(args []any) []any {
	out := append([]any{}, args...)
	for i, v := range out {
		if s, ok := v.(string); ok {
			out[i] = take(jsjson.UTF8Text(s))
		}
	}
	return out
}
