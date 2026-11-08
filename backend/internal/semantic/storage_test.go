package semantic

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type semanticFixture struct {
	main, external *sql.DB
	authority      *recovery.Authority
	store          *Store
}

func semMust(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func semSetup(t *testing.T) *semanticFixture {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	semMust(t, os.Mkdir(path, 0700))
	external, e := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
	semMust(t, e)
	_, e = external.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	semMust(t, e)
	semMust(t, os.Mkdir(filepath.Join(root, "live"), 0700))
	main, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite"))
	semMust(t, e)
	main.SetMaxOpenConns(1)
	_, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`)
	semMust(t, e)
	raw, e := json.Marshal(map[string]any{"head": map[string]any{"instanceId": "12345678-1234-4123-8123-123456789012", "sequence": 0, "hash": strings.Repeat("0", 64)}, "authority": path, "minimumReader": 1, "state": "ready"})
	semMust(t, e)
	_, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw))
	semMust(t, e)
	authority, e := recovery.OpenWriter(context.Background(), path)
	semMust(t, e)
	store, e := New(main, authority)
	semMust(t, e)
	t.Cleanup(func() { authority.Close(); main.Close(); external.Close() })
	return &semanticFixture{main, external, authority, store}
}
func (f *semanticFixture) run(fn func(*Store) error) error {
	return f.authority.WithWriteSession(context.Background(), f.main, func(s *recovery.WriteSession) error { return fn(f.store.WithSession(s)) })
}
func semCandidate(t *testing.T, id, first string) *jsjson.Object {
	t.Helper()
	raw := fmt.Sprintf(`{"schemaVersion":1,"id":%q,"sceneId":"scene","personId":null,"partitionKey":"%s","version":1,"inputKey":"%s","kind":"summary_candidate","text":"Synthetic declaration","selectedParentIds":[%q,"b"],"parents":[{"ref":{"id":%q,"version":1,"deletionEpoch":0},"dependencyKey":"%s","fingerprint":"%s","proofs":[{"proofId":"proof-a","sources":[],"parents":[]}],"roots":["%s"]},{"ref":{"id":"b","version":1,"deletionEpoch":0},"dependencyKey":"%s","fingerprint":"%s","proofs":[{"proofId":"proof-b","sources":[],"parents":[]}],"roots":["%s"]}],"anchorDependencies":[],"anchorQualifications":[],"hypothesis":false,"status":"candidate","admission":"qualified_candidate","createdAt":"2026-10-29T00:00:00Z","applicableUntil":null}`, id, strings.Repeat("a", 64), fmt.Sprintf("%064x", id), first, first, strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64))
	o, e := ParseCandidate(raw)
	semMust(t, e)
	return o
}
func semAttempt(t *testing.T) *jsjson.Object {
	t.Helper()
	o, e := ParseAttempt(fmt.Sprintf(`{"schemaVersion":1,"sceneId":"scene","inputKey":"%s","status":"started","version":1,"parents":[{"ref":{"id":"victim","version":1,"deletionEpoch":0},"dependencyKey":"%s"}],"roots":["%s"]}`, strings.Repeat("e", 64), strings.Repeat("a", 64), strings.Repeat("b", 64)))
	semMust(t, e)
	return o
}
func TestPurgeDurableOrderTombstoneAndBinding(t *testing.T) {
	f := semSetup(t)
	candidate := semCandidate(t, "one", "victim")
	attempt := semAttempt(t)
	semMust(t, f.run(func(s *Store) error {
		if e := s.Write(candidate, nil, "admit"); e != nil {
			return e
		}
		return s.WriteAttempt(attempt, nil)
	}))
	var escaped *Store
	semMust(t, f.run(func(s *Store) error {
		escaped = s
		return s.WithWriteSession(func(child *recovery.WriteSession) error {
			if child != s.session {
				t.Fatal("substituted child")
			}
			return child.WithSavepoint(func(nested *recovery.WriteSession) error { return s.WithSession(nested).PurgeAssertion("victim") })
		})
	}))
	if !errors.Is(escaped.RequireTransaction(), sql.ErrTxDone) {
		t.Fatal("escaped session accepted")
	}
	var version int
	var value any
	semMust(t, f.main.QueryRow("SELECT version,value FROM memory_semantic_candidates WHERE id='one'").Scan(&version, &value))
	if version != 2 || value != nil {
		t.Fatal("missing tombstone", version, value)
	}
	var reader int
	semMust(t, f.main.QueryRow("SELECT json_extract(value,'$.minimumReader') FROM recovery_gate").Scan(&reader))
	if reader != 7 {
		t.Fatal(reader)
	}
	events := semEvents(t, f, 2)
	if len(events) != 2 || semGet(events[0], "operation") != "attempt" || semGet(events[1], "operation") != "privacy" || semGet(events[1], "nextVersion") != nil || semGet(events[1], "afterHash") != nil {
		t.Fatal("wrong durable intents", events)
	}
	before, e := Hash(candidate)
	semMust(t, e)
	if semGet(events[1], "beforeHash") != before {
		t.Fatal("before hash mismatch")
	}
	semMust(t, f.run(func(s *Store) error {
		seen, e := s.Seen(semString(candidate, "inputKey"))
		if e != nil {
			return e
		}
		if !seen {
			t.Fatal("tombstone lost input key")
		}
		slots, e := s.AvailableSlots("scene")
		if e != nil {
			return e
		}
		if slots != 39 {
			t.Fatal(slots)
		}
		if e = s.Write(candidate, nil, "admit"); e == nil || !strings.Contains(e.Error(), "semantic_identity_reused") {
			t.Fatal(e)
		}
		return nil
	}))
}
func semEvents(t *testing.T, f *semanticFixture, after int) []*jsjson.Object {
	t.Helper()
	r, e := f.external.Query("SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence", after)
	semMust(t, e)
	defer r.Close()
	out := []*jsjson.Object{}
	for r.Next() {
		var raw string
		semMust(t, r.Scan(&raw))
		v, e := jsjson.Parse(raw)
		semMust(t, e)
		out = append(out, v.(*jsjson.Object))
	}
	semMust(t, r.Err())
	return out
}
func TestPurgeAbsentDoesNotEnroll(t *testing.T) {
	f := semSetup(t)
	semMust(t, f.run(func(s *Store) error { return s.PurgeAssertion("victim") }))
	var count int
	semMust(t, f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'memory_semantic_%'").Scan(&count))
	if count != 0 {
		t.Fatal("read initialized optional tables")
	}
	if len(semEvents(t, f, 0)) != 0 {
		t.Fatal("absent purge emitted intent")
	}
}
func TestPurgeRejectsReadiness(t *testing.T) {
	f := semSetup(t)
	_, e := f.main.Exec("UPDATE recovery_gate SET value=json_set(value,'$.state','quarantined')")
	semMust(t, e)
	called := false
	e = f.run(func(s *Store) error {
		called = true
		if e := s.session.CheckBinding(f.authority, f.main); e != nil {
			return e
		}
		e := s.RequireTransaction()
		if e == nil {
			t.Fatal("CheckBinding alone accepted non-ready gate")
		}
		return e
	})
	if e == nil || !called {
		t.Fatal("readiness check not exercised", e, called)
	}
}

func semProofCandidate(t *testing.T, id string, version float64) *jsjson.Object {
	t.Helper()
	o := semCandidate(t, id, "a")
	parents := semGet(o, "parents").([]any)
	parent := parents[0].(*jsjson.Object)
	proof := semGet(parent, "proofs").([]any)[0].(*jsjson.Object)
	drop := proof.Clone()
	_ = drop.Set("proofId", "drop")
	_ = drop.Set("parents", []any{"victim"})
	_ = parent.Set("proofs", []any{drop, proof})
	_ = o.Set("version", version)
	o, e := ValidateCandidate(o)
	semMust(t, e)
	return o
}
func TestPurgePlansAllCandidateEffectsBeforeWriting(t *testing.T) {
	f := semSetup(t)
	first := semProofCandidate(t, "a", 1)
	overflow := semProofCandidate(t, "z", 9007199254740991)
	// Seed pre-existing durable material directly: a valid max-safe revision may
	// exist, but its survivor transition cannot be represented by source schema.
	semMust(t, f.run(func(s *Store) error { return s.session.EnsureSemanticCandidatesSchema() }))
	for _, o := range []*jsjson.Object{first, overflow} {
		raw, e := jsjson.Stringify(o)
		semMust(t, e)
		_, e = f.main.Exec("INSERT INTO memory_semantic_candidates VALUES(?,?,?,?,?)", semString(o, "id"), semString(o, "sceneId"), semString(o, "inputKey"), semNumber(o, "version"), raw)
		semMust(t, e)
	}
	// Catching the validation failure still must not commit an earlier candidate
	// effect. This checks planning, not just outer transaction rollback.
	semMust(t, f.run(func(s *Store) error {
		if e := s.PurgeAssertion("victim"); e == nil {
			t.Fatal("overflow accepted")
		}
		return nil
	}))
	if len(semEvents(t, f, 0)) != 0 {
		t.Fatal("candidate intent emitted before complete effect validation")
	}
	var version int
	semMust(t, f.main.QueryRow("SELECT version FROM memory_semantic_candidates WHERE id='a'").Scan(&version))
	if version != 1 {
		t.Fatal(version)
	}
}
func TestPurgeIntentSurvivesLocalRollback(t *testing.T) {
	f := semSetup(t)
	candidate := semCandidate(t, "one", "victim")
	semMust(t, f.run(func(s *Store) error { return s.Write(candidate, nil, "admit") }))
	_, e := f.main.Exec(`CREATE TRIGGER synthetic_fault BEFORE UPDATE ON memory_semantic_candidates BEGIN SELECT RAISE(ABORT,'synthetic');END`)
	semMust(t, e)
	if e = f.run(func(s *Store) error { return s.PurgeAssertion("victim") }); e == nil {
		t.Fatal("local failure accepted")
	}
	events := semEvents(t, f, 1)
	if len(events) != 1 || semGet(events[0], "operation") != "privacy" {
		t.Fatal("durable intent missing")
	}
	var version int
	var value string
	semMust(t, f.main.QueryRow("SELECT version,value FROM memory_semantic_candidates WHERE id='one'").Scan(&version, &value))
	if version != 1 || value == "" {
		t.Fatal("local rollback lost original")
	}
	gate, e := recovery.ReadGate(context.Background(), f.main)
	semMust(t, e)
	if gate.Head.Sequence != 1 {
		t.Fatal("local ACK incorrectly advanced")
	}
	if e = f.run(func(s *Store) error { return s.RequireTransaction() }); e == nil {
		t.Fatal("durable gap accepted")
	}
}
func TestStrictStorageIdentityAndTombstones(t *testing.T) {
	o := semCandidate(t, "one", "a")
	raw, e := jsjson.Stringify(o)
	semMust(t, e)
	key := semString(o, "inputKey")
	for _, row := range [][5]any{{"wrong", "scene", key, int64(1), raw}, {"one", "wrong", key, int64(1), raw}, {"one", "scene", strings.Repeat("f", 64), int64(1), raw}, {"one", "scene", key, int64(2), raw}, {"one", "scene", key, int64(0), nil}, {"one", "scene", "bad", int64(1), nil}, {"one", "scene", key, float64(1.5), nil}, {"one", "scene", key, int64(1), []byte(raw)}} {
		if _, e := decodeCandidate(row[0], row[1], row[2], row[3], row[4]); e == nil {
			t.Fatal("invalid storage row accepted", row)
		}
	}
	if o, e := decodeCandidate("one", "scene", key, int64(2), nil); e != nil || o != nil {
		t.Fatal(o, e)
	}
}

func TestSemanticWrongOwnerAndSuspendedParent(t *testing.T) {
	f, other := semSetup(t), semSetup(t)
	ctx := context.Background()
	semMust(t, f.authority.WithWriteSession(ctx, f.main, func(parent *recovery.WriteSession) error {
		if e := other.store.WithSession(parent).RequireTransaction(); e == nil {
			t.Fatal("wrong authority/database accepted")
		}
		// Distinguish wrong database from wrong authority, rather than relying only
		// on a pair where both identities differ.
		wrongDB, e := New(other.main, f.authority)
		if e != nil {
			return e
		}
		if e := wrongDB.WithSession(parent).RequireTransaction(); e == nil {
			t.Fatal("wrong database accepted")
		}
		wrongAuthority, e := New(f.main, other.authority)
		if e != nil {
			return e
		}
		if e := wrongAuthority.WithSession(parent).RequireTransaction(); e == nil {
			t.Fatal("wrong authority accepted")
		}
		bound := f.store.WithSession(parent)
		return parent.WithSavepoint(func(child *recovery.WriteSession) error {
			if e := bound.RequireTransaction(); e == nil {
				t.Fatal("suspended parent accepted")
			}
			return f.store.WithSession(child).RequireTransaction()
		})
	}))
}

func TestAttemptIntentAndMixedPlanningFailureRollback(t *testing.T) {
	for _, mode := range []string{"attempt-local-failure", "candidate-planning-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := semSetup(t)
			attempt := semAttempt(t)
			semMust(t, f.run(func(s *Store) error { return s.WriteAttempt(attempt, nil) }))
			if mode == "attempt-local-failure" {
				_, e := f.main.Exec(`CREATE TRIGGER synthetic_attempt_fault BEFORE UPDATE ON memory_semantic_attempts BEGIN SELECT RAISE(ABORT,'synthetic attempt fault'); END`)
				semMust(t, e)
			} else {
				overflow := semProofCandidate(t, "overflow", 9007199254740991)
				semMust(t, f.run(func(s *Store) error { return s.session.EnsureSemanticCandidatesSchema() }))
				raw, e := jsjson.Stringify(overflow)
				semMust(t, e)
				_, e = f.main.Exec("INSERT INTO memory_semantic_candidates VALUES(?,?,?,?,?)", semString(overflow, "id"), semString(overflow, "sceneId"), semString(overflow, "inputKey"), semNumber(overflow, "version"), raw)
				semMust(t, e)
			}
			if e := f.run(func(s *Store) error { return s.PurgeAssertion("victim") }); e == nil {
				t.Fatal("expected actual fault")
			}
			events := semEvents(t, f, 1)
			if len(events) != 1 || semGet(events[0], "operation") != "attempt" {
				t.Fatal("attempt durable prefix differs", events)
			}
			var raw string
			semMust(t, f.main.QueryRow("SELECT value FROM memory_semantic_attempts").Scan(&raw))
			actual, e := ParseAttempt(raw)
			semMust(t, e)
			if semNumber(actual, "version") != 1 || semGet(actual, "status") != "started" {
				t.Fatal("attempt local write escaped owner rollback")
			}
			gate, e := recovery.ReadGate(context.Background(), f.main)
			semMust(t, e)
			if gate.Head.Sequence != 1 {
				t.Fatal("failed business transaction advanced ACK", gate)
			}
			if e := f.run(func(s *Store) error { return s.RequireTransaction() }); e == nil {
				t.Fatal("external intent gap accepted")
			}
		})
	}
}

func TestCandidateCapacityCountsRetainedTombstones(t *testing.T) {
	for _, scope := range []string{"scene", "global"} {
		t.Run(scope, func(t *testing.T) {
			f := semSetup(t)
			semMust(t, f.run(func(s *Store) error { return s.session.EnsureSemanticCandidatesSchema() }))
			total, scene := 40, "scene"
			if scope == "global" {
				total, scene = 4096, "other"
			}
			tx, e := f.main.Begin()
			semMust(t, e)
			statement, e := tx.Prepare("INSERT INTO memory_semantic_candidates VALUES(?,?,?,?,NULL)")
			semMust(t, e)
			for i := 0; i < total; i++ {
				_, e = statement.Exec(fmt.Sprintf("tombstone-%d", i), scene, fmt.Sprintf("%064x", i+1), 1)
				semMust(t, e)
			}
			semMust(t, statement.Close())
			semMust(t, tx.Commit())
			semMust(t, f.run(func(s *Store) error {
				slots, e := s.AvailableSlots("scene")
				if e != nil {
					return e
				}
				if slots != 0 {
					t.Fatal("tombstones dropped from capacity", slots)
				}
				e = s.Write(semCandidate(t, "new", "a"), nil, "admit")
				if e == nil || !strings.Contains(e.Error(), "semantic_capacity_or_duplicate") {
					t.Fatal("capacity result", e)
				}
				return nil
			}))
			if len(semEvents(t, f, 0)) != 0 {
				t.Fatal("rejected admission wrote intent")
			}
		})
	}
}

func TestSemanticDuplicateAndPreviousHashConflict(t *testing.T) {
	f := semSetup(t)
	first := semCandidate(t, "one", "a")
	semMust(t, f.run(func(s *Store) error { return s.Write(first, nil, "admit") }))
	semMust(t, f.run(func(s *Store) error {
		duplicate := semCandidate(t, "two", "a")
		_ = duplicate.Set("inputKey", semGet(first, "inputKey"))
		if e := s.Write(duplicate, nil, "admit"); e == nil || !strings.Contains(e.Error(), "semantic_capacity_or_duplicate") {
			t.Fatal("duplicate accepted", e)
		}
		stale := first.Clone()
		_ = stale.Set("text", "Different prior body")
		next := first.Clone()
		_ = next.Set("version", float64(2))
		if e := s.Write(next, stale, "privacy"); e == nil || !strings.Contains(e.Error(), "semantic_version_conflict") {
			t.Fatal("previous hash conflict accepted", e)
		}
		return nil
	}))
	if len(semEvents(t, f, 1)) != 0 {
		t.Fatal("rejected conflicts appended intent")
	}
}
