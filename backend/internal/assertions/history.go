package assertions

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type State struct {
	Version       int64 `json:"version"`
	DeletionEpoch int64 `json:"deletionEpoch"`
	Deleted       bool  `json:"deleted"`
}
type HistoryPage struct {
	Revisions           []*Object `json:"revisions"`
	HasMore             bool      `json:"hasMore"`
	UnknownRecordedTime bool      `json:"unknownRecordedTime"`
	RetentionIncomplete bool      `json:"retentionIncomplete"`
}

func safe(v any, min float64) bool {
	n, ok := v.(float64)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= min && n <= 9007199254740991 && math.Trunc(n) == n
}
func digest(s string) string { b := sha256.Sum256([]byte(s)); return fmt.Sprintf("%x", b) }
func (r *Reader) State(id string) (state State, err error) {
	var deleted int
	_, err = r.one("SELECT version,deletion_epoch,deleted FROM memory_assertion_state WHERE assertion_id=?", []any{id}, &state.Version, &state.DeletionEpoch, &deleted)
	state.Deleted = deleted != 0
	return
}
func decodeRevision(id string, version int64, key, raw string) *Object {
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil
	}
	o, ok := v.(*Object)
	if !ok || o == nil {
		return nil
	}
	record, ok := get(o, "record").(*Object)
	if !ok || record == nil {
		return nil
	}
	kind := get(get(record, "scope"), "kind")
	if get(o, "assertionId") != id || get(o, "version") != float64(version) || get(record, "id") != id || (kind != "scene" && kind != "sharedPublicKnowledge") || scope(record) != key {
		return nil
	}
	return o
}
func (r *Reader) Revision(id string, version float64, key string) (*Object, error) {
	if !safe(version, 1) {
		return nil, nil
	}
	state, e := r.State(id)
	if e != nil || state.Deleted {
		return nil, e
	}
	var raw string
	found, e := r.one("SELECT value FROM memory_assertion_revisions WHERE assertion_id=? AND version=? AND scope_key=? AND value IS NOT NULL", []any{id, int64(version), key}, &raw)
	if e != nil || !found {
		return nil, e
	}
	return decodeRevision(id, int64(version), key, raw), nil
}
func (s *Store) Append(input *Object) (*Object, error) {
	if e := s.requireTransaction(); e != nil {
		return nil, e
	}
	record, ok := get(input, "record").(*Object)
	if !ok || record == nil {
		return nil, errors.New("invalid_assertion_revision")
	}
	id := str(get(record, "id"))
	key := str(get(input, "idempotencyKey"))
	units, e := jsjson.UTF16(key)
	if e != nil {
		return nil, e
	}
	timesValid := true
	for _, name := range []string{"recordedAt", "validFrom", "validTo"} {
		v, present := input.Get(name)
		timesValid = timesValid && present && validTime(v)
	}
	from, to := get(input, "validFrom"), get(input, "validTo")
	if id == "" || key == "" || len(units) > 256 || !safe(get(input, "expectedVersion"), 0) || !safe(get(input, "expectedDeletionEpoch"), 0) || !timesValid || (from != nil && to != nil && str(from) > str(to)) {
		return nil, errors.New("invalid_assertion_revision")
	}
	state, e := s.reader().State(id)
	if e != nil {
		return nil, e
	}
	if state.Deleted || float64(state.DeletionEpoch) != get(input, "expectedDeletionEpoch") {
		return nil, errors.New("assertion_deleted_or_stale_epoch")
	}
	legacy := get(input, "legacyImport")
	if legacy == nil {
		legacy = false
	}
	payload := obj("record", record, "supports", get(input, "supports"), "recordedAt", get(input, "recordedAt"), "validFrom", from, "validTo", to, "event", obj("kind", get(input, "eventKind"), "actorId", get(input, "actorId"), "legacyImport", legacy))
	raw, e := jsjson.Stringify(payload)
	if e != nil {
		return nil, e
	}
	hash := digest(raw)
	var priorHash, priorValue sql.NullString
	found, e := s.reader().one("SELECT payload_hash,value FROM memory_assertion_revisions WHERE assertion_id=? AND idempotency_key=?", []any{id, key}, &priorHash, &priorValue)
	if e != nil {
		return nil, e
	}
	if found {
		if !priorHash.Valid || priorHash.String != hash || !priorValue.Valid || priorValue.String == "" {
			return nil, errors.New("assertion_idempotency_conflict")
		}
		v, e := jsjson.Parse(priorValue.String)
		if e != nil {
			return nil, e
		}
		o, ok := v.(*Object)
		if !ok {
			return nil, errors.New("assertion_revision_object_required")
		}
		return o, nil
	}
	if float64(state.Version) != get(input, "expectedVersion") {
		return nil, errors.New("assertion_version_conflict")
	}
	var priorScope string
	found, e = s.reader().one("SELECT scope_key FROM memory_assertion_revisions WHERE assertion_id=? AND value IS NOT NULL LIMIT 1", []any{id}, &priorScope)
	if e != nil {
		return nil, e
	}
	if found && priorScope != scope(record) {
		return nil, errors.New("assertion_scope_conflict")
	}
	revision := obj("assertionId", id, "version", float64(state.Version+1))
	for _, k := range []string{"record", "supports", "recordedAt", "validFrom", "validTo", "event"} {
		_ = revision.Set(k, get(payload, k))
	}
	raw, e = jsjson.Stringify(revision)
	if e != nil {
		return nil, e
	}
	if e = s.exec("INSERT INTO memory_assertion_state VALUES(?,?,?,0) ON CONFLICT(assertion_id) DO UPDATE SET version=excluded.version", id, state.Version+1, state.DeletionEpoch); e != nil {
		return nil, e
	}
	if e = s.exec("INSERT INTO memory_assertion_revisions VALUES(?,?,?,?,?,?,?)", id, state.Version+1, scope(record), get(input, "recordedAt"), key, hash, raw); e != nil {
		return nil, e
	}
	sources := append([]any{}, array(get(record, "sources"))...)
	proofs := array(get(input, "supports"))
	for _, p := range proofs {
		sources = append(sources, array(get(p, "sources"))...)
	}
	for _, source := range sources {
		if e = s.exec("INSERT OR IGNORE INTO memory_assertion_revision_sources VALUES(?,?,?,?)", id, state.Version+1, get(source, "sceneId"), get(source, "messageId")); e != nil {
			return nil, e
		}
	}
	for _, p := range proofs {
		for _, parent := range array(get(p, "parents")) {
			if e = s.exec("INSERT OR IGNORE INTO memory_assertion_revision_parents VALUES(?,?,?)", id, state.Version+1, parent); e != nil {
				return nil, e
			}
		}
	}
	detached, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	return detached.(*Object), nil
}
func (s *Store) ImportLegacy(record *Object, supports []any) (State, error) {
	if e := s.requireTransaction(); e != nil {
		return State{}, e
	}
	state, e := s.reader().State(str(get(record, "id")))
	if e != nil {
		return state, e
	}
	if state.Deleted {
		return state, errors.New("assertion_deleted_or_stale_epoch")
	}
	if state.Version > 0 {
		return state, nil
	}
	var at, from, to any
	v, ok := record.Get("learnedAt")
	if ok && validTime(v) {
		at = v
	}
	if v = get(record, "validFrom"); validTime(v) {
		from = v
	}
	if v = get(record, "validUntil"); validTime(v) {
		to = v
	}
	kind := "learned_assertion"
	if get(record, "origin") == "manual" {
		kind = "admin_assertion"
	}
	_, e = s.Append(obj("record", record, "supports", supports, "expectedVersion", float64(0), "expectedDeletionEpoch", float64(0), "idempotencyKey", "legacy-import-v1", "recordedAt", at, "validFrom", from, "validTo", to, "eventKind", kind, "legacyImport", true))
	if e != nil {
		return State{}, e
	}
	return s.reader().State(str(get(record, "id")))
}
func (r *Reader) RetainedCount(id, key string) (int64, error) {
	state, e := r.State(id)
	if e != nil || state.Deleted {
		return 0, e
	}
	var count int64
	_, e = r.one("SELECT COUNT(*) FROM memory_assertion_revisions WHERE assertion_id=? AND scope_key=? AND value IS NOT NULL", []any{id, key}, &count)
	return count, e
}
func (r *Reader) Revisions(id, key string, limit *float64) (out []*Object, err error) {
	out = []*Object{}
	if limit != nil && (!safe(*limit, 1) || *limit > 10000) {
		return nil, errors.New("assertion_history_limit_invalid")
	}
	state, err := r.State(id)
	if err != nil || state.Deleted {
		return out, err
	}
	order, n := "ASC", int64(-1)
	if limit != nil {
		order, n = "DESC", int64(*limit)
	}
	rows, err := r.query("SELECT version,value FROM memory_assertion_revisions WHERE assertion_id=? AND scope_key=? AND value IS NOT NULL ORDER BY version "+order+" LIMIT ?", id, key, n)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var version int64
		var raw string
		if err = rows.Scan(&version, &raw); err != nil {
			return
		}
		if v := decodeRevision(id, version, key, raw); v != nil {
			out = append(out, v)
		}
	}
	if err = rows.Err(); err != nil {
		return
	}
	if limit != nil {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return
}
func (r *Reader) QueryRetained(id, key string, query *Object) (page HistoryPage, err error) {
	page.Revisions = []*Object{}
	version, limit := get(query, "throughVersion"), get(query, "limit")
	asOf, hasAsOf := query.Get("asOfRecorded")
	validAt, hasValid := query.Get("validAt")
	if !safe(version, 1) || !safe(limit, 1) || limit.(float64) > 20 || (hasAsOf && !validTime(asOf)) || (hasValid && !validTime(validAt)) {
		return page, errors.New("assertion_history_query_invalid")
	}
	state, err := r.State(id)
	if err != nil {
		return
	}
	if state.Deleted {
		page.RetentionIncomplete = true
		return
	}
	base := "assertion_id=? AND scope_key=? AND version<=? AND value IS NOT NULL"
	args := []any{id, key, int64(version.(float64))}
	var count int64
	_, err = r.one("SELECT COUNT(*) FROM memory_assertion_revisions WHERE "+base, args, &count)
	if err != nil {
		return
	}
	var one int
	page.UnknownRecordedTime, err = r.one("SELECT 1 FROM memory_assertion_revisions WHERE "+base+" AND recorded_at IS NULL LIMIT 1", args, &one)
	if err != nil {
		return
	}
	malformed, err := r.one("SELECT 1 FROM memory_assertion_revisions WHERE "+base+" AND NOT json_valid(value) LIMIT 1", args, &one)
	if err != nil {
		return
	}
	predicates := []string{base, "json_valid(value)"}
	params := append([]any{}, args...)
	if hasAsOf {
		predicates = append(predicates, "recorded_at IS NOT NULL AND recorded_at<=?")
		params = append(params, asOf)
	}
	if hasValid {
		predicates = append(predicates, "(json_extract(value,'$.validFrom') IS NULL OR json_extract(value,'$.validFrom')<=?) AND (json_extract(value,'$.validTo') IS NULL OR json_extract(value,'$.validTo')>?)")
		params = append(params, validAt, validAt)
	}
	n := int(limit.(float64))
	params = append(params, n+1)
	rows, err := r.query("SELECT version,recorded_at,value FROM memory_assertion_revisions WHERE "+strings.Join(predicates, " AND ")+" ORDER BY version DESC LIMIT ?", params...)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	total := 0
	invalid := malformed
	for rows.Next() {
		var v int64
		var at sql.NullString
		var raw string
		if err = rows.Scan(&v, &at, &raw); err != nil {
			return
		}
		total++
		if total > n {
			continue
		}
		o := decodeRevision(id, v, key, raw)
		var recorded any
		if at.Valid {
			recorded = at.String
		}
		_, legacyBool := get(get(o, "event"), "legacyImport").(bool)
		validFields := o != nil
		for _, k := range []string{"recordedAt", "validFrom", "validTo"} {
			if o == nil {
				break
			}
			value, present := o.Get(k)
			validFields = validFields && present && validTime(value)
		}
		if !validFields || get(o, "recordedAt") != recorded || !legacyBool {
			invalid = true
			continue
		}
		page.Revisions = append(page.Revisions, o)
	}
	if err = rows.Err(); err != nil {
		return
	}
	for i, j := 0, len(page.Revisions)-1; i < j; i, j = i+1, j-1 {
		page.Revisions[i], page.Revisions[j] = page.Revisions[j], page.Revisions[i]
	}
	page.HasMore = total > n
	expected := state.Version
	if v := int64(version.(float64)); v < expected {
		expected = v
	}
	page.RetentionIncomplete = invalid || count < expected
	return
}
func (r *Reader) Dependents(id string) ([]string, error) {
	return r.ids("SELECT DISTINCT parent.assertion_id FROM memory_assertion_revision_parents AS parent JOIN memory_assertion_revisions AS revision ON revision.assertion_id=parent.assertion_id AND revision.version=parent.version WHERE parent.parent_id=? AND revision.value IS NOT NULL ORDER BY parent.assertion_id", id)
}
func (r *Reader) ids(q string, args ...any) (out []string, err error) {
	out = []string{}
	rows, err := r.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return
		}
		out = append(out, id)
	}
	err = rows.Err()
	return
}
func (s *Store) RedactSource(scene, id string) ([]string, error) {
	if e := s.requireTransaction(); e != nil {
		return nil, e
	}
	ids, e := s.reader().ids("SELECT DISTINCT assertion_id FROM memory_assertion_revision_sources WHERE scene_id=? AND message_id=?", scene, id)
	if e != nil {
		return nil, e
	}
	for _, id := range ids {
		if _, e = s.Purge(id); e != nil {
			return nil, e
		}
	}
	return ids, nil
}
func (s *Store) Purge(id string) (State, error) { return s.erase(id, false) }
func (s *Store) Erase(id string) (State, error) { return s.erase(id, true) }
func (s *Store) erase(id string, terminal bool) (State, error) {
	if e := s.requireTransaction(); e != nil {
		return State{}, e
	}
	state, e := s.reader().State(id)
	if e != nil {
		return state, e
	}
	if _, e = s.session.AppendIntent(recovery.Assertion{ID: id, ThroughVersion: state.Version, Terminal: terminal}); e != nil {
		return state, e
	}
	if state.Deleted {
		return state, nil
	}
	deleted, update := 0, "deletion_epoch=excluded.deletion_epoch"
	if terminal {
		deleted = 1
		update += ",deleted=1"
	}
	if e = s.exec("INSERT INTO memory_assertion_state VALUES(?,?,?,?) ON CONFLICT(assertion_id) DO UPDATE SET "+update, id, state.Version, state.DeletionEpoch+1, deleted); e != nil {
		return state, e
	}
	for _, table := range []string{"memory_assertion_revision_sources", "memory_assertion_revision_parents"} {
		if e = s.exec("DELETE FROM "+table+" WHERE assertion_id=?", id); e != nil {
			return state, e
		}
	}
	q := "UPDATE memory_assertion_revisions SET value=NULL,idempotency_key=NULL,payload_hash=NULL WHERE assertion_id=?"
	if terminal {
		q = "DELETE FROM memory_assertion_revisions WHERE assertion_id=?"
	}
	if e = s.exec(q, id); e != nil {
		return state, e
	}
	return s.reader().State(id)
}

// LatestRevision is the final decoded revision from Revisions, without decoding
// the entire retained history on every coordinator flush. Malformed trailing
// rows are skipped exactly as Revisions skips them, rather than LIMIT 1 hiding
// an earlier valid revision. This adds no retention or admission limit.
func (r *Reader) LatestRevision(id, key string) (out *Object, err error) {
	state, e := r.State(id)
	if e != nil || state.Deleted {
		return nil, e
	}
	rows, e := r.query("SELECT version,value FROM memory_assertion_revisions WHERE assertion_id=? AND scope_key=? AND value IS NOT NULL ORDER BY version DESC", id, key)
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var version int64
		var raw string
		if e = rows.Scan(&version, &raw); e != nil {
			return nil, e
		}
		if value := decodeRevision(id, version, key, raw); value != nil {
			return value, nil
		}
	}
	return nil, rows.Err()
}
