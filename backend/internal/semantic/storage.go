// Package semantic owns live semantic storage and privacy purge. Admission,
// requalification and offline recovery are deliberately separate domains.
package semantic

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"regexp"
)

type Store struct {
	main         *sql.DB
	authority    *recovery.Authority
	session      *recovery.WriteSession
	bindingError error
}

func New(main *sql.DB, authority *recovery.Authority) (*Store, error) {
	if main == nil || authority == nil {
		return nil, errors.New("semantic_authority_required")
	}
	return &Store{main: main, authority: authority}, nil
}
func (s *Store) WithSession(session *recovery.WriteSession) *Store {
	n := *s
	n.session = session
	n.bindingError = nil
	if session == nil {
		n.bindingError = errors.New("semantic_transaction_required")
	} else {
		n.bindingError = session.CheckBinding(s.authority, s.main)
	}
	return &n
}
func (s *Store) RequireTransaction() error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session == nil {
		return errors.New("semantic_transaction_required")
	}
	if e := s.session.CheckBinding(s.authority, s.main); e != nil {
		return e
	}
	return s.session.AssertReady()
}

// WithWriteSession borrows exactly the currently bound child, never a sibling
// session or an independently committed transaction.
func (s *Store) WithWriteSession(fn func(*recovery.WriteSession) error) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if fn == nil {
		return errors.New("semantic_callback_required")
	}
	return errors.Join(fn(s.session), s.RequireTransaction())
}
func semGet(o *jsjson.Object, k string) any        { v, _ := o.Get(k); return v }
func semString(o *jsjson.Object, k string) string  { return semGet(o, k).(string) }
func semNumber(o *jsjson.Object, k string) float64 { return semGet(o, k).(float64) }
func Hash(o *jsjson.Object) (string, error) {
	var v any = o
	if o == nil {
		v = nil
	}
	b, e := jsjson.Marshal(v)
	if e != nil {
		return "", e
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
func sameHash(a, b *jsjson.Object) (bool, error) {
	x, e := Hash(a)
	if e != nil {
		return false, e
	}
	y, e := Hash(b)
	return x == y, e
}
func semBind(args []any) []any {
	out := append([]any{}, args...)
	for i, v := range out {
		if s, ok := v.(string); ok {
			if t, e := jsjson.UTF8Text(s); e == nil {
				out[i] = t
			}
		}
	}
	return out
}
func (s *Store) exists(table string) (bool, error) {
	if e := s.RequireTransaction(); e != nil {
		return false, e
	}
	r, e := s.session.Query("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", table)
	if e != nil {
		return false, e
	}
	found := r.Next()
	return found, errors.Join(r.Err(), r.Close())
}

var storageHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validStorageID(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	n, e := jsjson.UTF16Len(s)
	return e == nil && n > 0 && n <= 512
}
func decodeCandidate(id, scene, key, version, value any) (*jsjson.Object, error) {
	k, ok := key.(string)
	v, number := version.(int64)
	if !validStorageID(id) || !validStorageID(scene) || !ok || !storageHash.MatchString(k) || !number || v < 1 || v > 9007199254740991 {
		return nil, errors.New("semantic_storage_invalid")
	}
	if value == nil {
		return nil, nil
	}
	raw, ok := value.(string)
	if !ok {
		return nil, errors.New("semantic_storage_invalid")
	}
	o, e := ParseCandidate(raw)
	if e != nil {
		return nil, e
	}
	if semGet(o, "id") != id || semGet(o, "sceneId") != scene || semGet(o, "inputKey") != key || semNumber(o, "version") != float64(v) {
		return nil, errors.New("semantic_storage_identity_invalid")
	}
	return o, nil
}
func (s *Store) candidates(where string, args []any, limit int) (out []*jsjson.Object, err error) {
	out = []*jsjson.Object{}
	yes, e := s.exists("memory_semantic_candidates")
	if e != nil || !yes {
		return out, e
	}
	r, e := s.session.Query("SELECT id,scene_id,input_key,version,value FROM memory_semantic_candidates"+where, semBind(args)...)
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, r.Err(), r.Close()) }()
	raw := [][5]any{}
	for r.Next() {
		var v [5]any
		if err = r.Scan(&v[0], &v[1], &v[2], &v[3], &v[4]); err != nil {
			return nil, err
		}
		raw = append(raw, v)
	}
	if limit > 0 && len(raw) > limit {
		return nil, errors.New("semantic_storage_capacity_invalid")
	}
	for _, v := range raw {
		o, e := decodeCandidate(v[0], v[1], v[2], v[3], v[4])
		if e != nil {
			return nil, e
		}
		if o != nil {
			out = append(out, o)
		}
	}
	return out, nil
}
func (s *Store) Rows(scene *string) ([]*jsjson.Object, error) {
	if scene == nil {
		return s.candidates(" ORDER BY id", nil, 4096)
	}
	return s.candidates(" WHERE scene_id=? ORDER BY id", []any{*scene}, 40)
}
func (s *Store) Get(id string) (*jsjson.Object, error) {
	r, e := s.candidates(" WHERE id=?", []any{id}, 0)
	if e != nil || len(r) == 0 {
		return nil, e
	}
	return r[0], nil
}
func (s *Store) candidateExists(column, value string) (bool, error) {
	yes, e := s.exists("memory_semantic_candidates")
	if e != nil || !yes {
		return false, e
	}
	r, e := s.session.Query("SELECT 1 FROM memory_semantic_candidates WHERE "+column+"=?", semBind([]any{value})...)
	if e != nil {
		return false, e
	}
	found := r.Next()
	return found, errors.Join(r.Err(), r.Close())
}
func (s *Store) Seen(key string) (bool, error) { return s.candidateExists("input_key", key) }
func (s *Store) AvailableSlots(scene string) (int, error) {
	yes, e := s.exists("memory_semantic_candidates")
	if e != nil || !yes {
		return 40, e
	}
	// Explicit main qualification preserves SQLite count fast-path authority metadata.
	r, e := s.session.Query("SELECT (SELECT COUNT(*) FROM main.memory_semantic_candidates WHERE scene_id=?),COUNT(*) FROM main.memory_semantic_candidates", semBind([]any{scene})...)
	if e != nil {
		return 0, e
	}
	var a, b int
	if r.Next() {
		e = r.Scan(&a, &b)
	}
	e = errors.Join(e, r.Err(), r.Close())
	return max(0, min(40-a, 4096-b)), e
}
func (s *Store) Attempts() (out []*jsjson.Object, err error) {
	out = []*jsjson.Object{}
	yes, e := s.exists("memory_semantic_attempts")
	if e != nil || !yes {
		return out, e
	}
	r, e := s.session.Query("SELECT scene_id,input_key,value FROM memory_semantic_attempts ORDER BY input_key")
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, r.Err(), r.Close()) }()
	raw := [][3]any{}
	for r.Next() {
		var v [3]any
		if err = r.Scan(&v[0], &v[1], &v[2]); err != nil {
			return nil, err
		}
		raw = append(raw, v)
	}
	if len(raw) > 4096 {
		return nil, errors.New("semantic_attempt_capacity_invalid")
	}
	for _, v := range raw {
		text, ok := v[2].(string)
		if !ok {
			return nil, errors.New("semantic_attempt_invalid")
		}
		o, e := ParseAttempt(text)
		if e != nil {
			return nil, e
		}
		if semGet(o, "sceneId") != v[0] || semGet(o, "inputKey") != v[1] {
			return nil, errors.New("semantic_attempt_identity_invalid")
		}
		out = append(out, o)
	}
	return out, nil
}
func mutation(next, previous *jsjson.Object, operation string) (recovery.SemanticMutation, error) {
	m := recovery.SemanticMutation{Operation: operation}
	identity := next
	if identity == nil {
		identity = previous
	}
	m.ID = semString(identity, "id")
	m.SceneID = semString(identity, "sceneId")
	if previous != nil {
		v := semNumber(previous, "version")
		m.PreviousVersion = &v
		h, e := Hash(previous)
		if e != nil {
			return m, e
		}
		m.BeforeHash = &h
	}
	if next != nil {
		v := semNumber(next, "version")
		m.NextVersion = &v
		h, e := Hash(next)
		if e != nil {
			return m, e
		}
		m.AfterHash = &h
		if operation == "admit" || operation == "review" {
			m.NextState = &recovery.SemanticNextState{Status: semString(next, "status"), Admission: semString(next, "admission")}
		}
	}
	return m, nil
}
func (s *Store) record(m recovery.SemanticMutation) error {
	if e := s.session.RequireReader(7); e != nil {
		return e
	}
	_, e := s.session.AppendIntent(m)
	return e
}
func (s *Store) Write(next, previous *jsjson.Object, operation string) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if next == nil && previous == nil {
		return errors.New("semantic_write_empty")
	}
	var e error
	if next != nil {
		next, e = ValidateCandidate(next)
		if e != nil {
			return e
		}
	}
	identity := next
	if identity == nil {
		identity = previous
	}
	current, e := s.Get(semString(identity, "id"))
	if e != nil {
		return e
	}
	equal, e := sameHash(current, previous)
	if e != nil {
		return e
	}
	if !equal {
		return errors.New("semantic_version_conflict")
	}
	if next != nil && previous != nil && (semGet(next, "id") != semGet(previous, "id") || semGet(next, "sceneId") != semGet(previous, "sceneId") || semGet(next, "inputKey") != semGet(previous, "inputKey") || semNumber(next, "version") != semNumber(previous, "version")+1) {
		return errors.New("semantic_version_conflict")
	}
	if next != nil && previous == nil {
		yes, e := s.candidateExists("id", semString(next, "id"))
		if e != nil {
			return e
		}
		if yes {
			return errors.New("semantic_identity_reused")
		}
		seen, e := s.Seen(semString(next, "inputKey"))
		if e != nil {
			return e
		}
		slots, e := s.AvailableSlots(semString(next, "sceneId"))
		if e != nil {
			return e
		}
		if semNumber(next, "version") != 1 || seen || slots < 1 {
			return errors.New("semantic_capacity_or_duplicate")
		}
	}
	m, e := mutation(next, previous, operation)
	if e != nil {
		return e
	}
	if e = s.record(m); e != nil {
		return e
	}
	if e = s.session.EnsureSemanticCandidatesSchema(); e != nil {
		return e
	}
	if next != nil {
		raw, e := jsjson.Stringify(next)
		if e != nil {
			return e
		}
		_, e = s.session.Exec("INSERT INTO memory_semantic_candidates(id,scene_id,input_key,version,value) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version,value=excluded.value", semBind([]any{semGet(next, "id"), semGet(next, "sceneId"), semGet(next, "inputKey"), semGet(next, "version"), raw})...)
		return e
	}
	_, e = s.session.Exec("UPDATE memory_semantic_candidates SET version=version+1,value=NULL WHERE id=?", semBind([]any{semGet(previous, "id")})...)
	return e
}
func (s *Store) WriteAttempt(next, previous *jsjson.Object) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	next, e := ValidateAttempt(next)
	if e != nil {
		return e
	}
	rows, e := s.Attempts()
	if e != nil {
		return e
	}
	var current *jsjson.Object
	for _, r := range rows {
		if semGet(r, "inputKey") == semGet(next, "inputKey") {
			current = r
			break
		}
	}
	equal, e := sameHash(current, previous)
	if e != nil {
		return e
	}
	if !equal {
		return errors.New("semantic_attempt_conflict")
	}
	if previous == nil && (len(rows) >= 4096 || semNumber(next, "version") != 1) {
		return errors.New("semantic_attempt_capacity")
	}
	if previous != nil && (semGet(previous, "sceneId") != semGet(next, "sceneId") || semGet(previous, "inputKey") != semGet(next, "inputKey") || semNumber(next, "version") != semNumber(previous, "version")+1) {
		return errors.New("semantic_attempt_conflict")
	}
	// Attempts have inputKey instead of candidate id. Build the same typed intent
	// without changing the canonical objects hashed into before/after witnesses.
	m := recovery.SemanticMutation{Operation: "attempt", ID: semString(next, "inputKey"), SceneID: semString(next, "sceneId")}
	v := semNumber(next, "version")
	m.NextVersion = &v
	h, e := Hash(next)
	if e != nil {
		return e
	}
	m.AfterHash = &h
	if previous != nil {
		v := semNumber(previous, "version")
		m.PreviousVersion = &v
		h, e := Hash(previous)
		if e != nil {
			return e
		}
		m.BeforeHash = &h
	}
	if e = s.record(m); e != nil {
		return e
	}
	if e = s.session.EnsureSemanticAttemptsSchema(); e != nil {
		return e
	}
	raw, e := jsjson.Stringify(next)
	if e != nil {
		return e
	}
	_, e = s.session.Exec("INSERT INTO memory_semantic_attempts(scene_id,input_key,value) VALUES(?,?,?) ON CONFLICT(input_key) DO UPDATE SET value=excluded.value", semBind([]any{semGet(next, "sceneId"), semGet(next, "inputKey"), raw})...)
	return e
}
