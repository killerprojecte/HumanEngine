package commitments

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"strings"
	"testing"
)

func sample(t *testing.T) *Object { return object(get(loadOracle(t), "readSample")) }
func seeded(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	trace := arr(get(loadOracle(t), "traces"))[0]
	seedRows(t, f, object(parse(t, str(get(trace, "initial")))))
	return f
}
func TestInitializationReadAndRequiredOwner(t *testing.T) {
	f := setup(t)
	if _, e := NewPrivacyService(f.store, Owners{}); e == nil {
		t.Fatal("missing owner accepted")
	}
	e := f.store.Read(context.Background(), func(r *Reader) error { _, e := r.Head("id"); return e })
	if e == nil {
		t.Fatal("uninitialized source SQL read unexpectedly succeeded")
	}
	var n int
	must(t, f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'memory_commitment_%'").Scan(&n))
	if n != 0 {
		t.Fatal("read created schema")
	}
	must(t, f.store.Initialize(context.Background()))
	must(t, f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE 'memory_commitment_%'").Scan(&n))
	if n != 3 {
		t.Fatalf("base initialization created %d tables", n)
	}
	must(t, f.store.Read(context.Background(), func(r *Reader) error {
		value, e := r.Timeline("absent")
		if value != nil {
			t.Fatal("missing history invented")
		}
		return e
	}))
	must(t, f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE 'memory_commitment_%'").Scan(&n))
	if n != 3 {
		t.Fatal("timeline read created schema")
	}
}
func TestActualSourceStagedSchemaFailures(t *testing.T) {
	for _, v := range arr(get(loadOracle(t), "schemaFaults")) {
		v := object(v)
		t.Run(str(get(v, "name")), func(t *testing.T) {
			f := setup(t)
			var caught error
			if get(v, "name") == "base-atomic" {
				_, e := f.main.Exec("CREATE TABLE memory_commitment_heads(bad TEXT)")
				must(t, e)
				must(t, f.store.Transaction(context.Background(), func(s *Store) error { caught = s.Initialize(context.Background()); return nil }))
			} else {
				_, e := f.main.Exec("CREATE TABLE memory_commitment_temporal_windows(bad TEXT)")
				must(t, e)
				must(t, f.store.Transaction(context.Background(), func(s *Store) error {
					snapshot := sample(t)
					caught = s.ReplaceTimeline(str(get(get(snapshot, "head"), "id")), object(get(snapshot, "timeline")))
					return nil
				}))
			}
			if caught == nil {
				t.Fatal("staged schema fault was hidden")
			}
			rows := rowsValue(t, f.main, "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'memory_commitment_%' ORDER BY name")
			names := []any{}
			for _, r := range rows {
				names = append(names, get(r, "name"))
			}
			if stringify(t, names) != stringify(t, get(v, "tables")) {
				t.Fatalf("caught prefix %s want %s; %v", stringify(t, names), stringify(t, get(v, "tables")), caught)
			}
		})
	}
}
func TestSameRootChildBindingAndExpiration(t *testing.T) {
	f := seeded(t)
	other := setup(t)
	var escaped *PrivacyService
	must(t, f.store.Transaction(context.Background(), func(parent *Store) error {
		p, _ := NewPrivacyService(parent, f.service.owners)
		return parent.Transaction(context.Background(), func(child *Store) error {
			if e := p.PurgeSource("scene", "root"); e == nil {
				t.Fatal("suspended parent accepted")
			}
			return child.WithWriteSession(func(session *recovery.WriteSession) error {
				if e := other.service.WithSession(session).PurgeSource("scene", "root"); e == nil {
					t.Fatal("wrong database accepted")
				}
				escaped = f.service.WithSession(session)
				return escaped.PurgeSource("scene", "root")
			})
		})
	}))
	if e := escaped.PurgeSource("scene", "root"); e == nil {
		t.Fatal("expired child accepted")
	}
	must(t, f.store.Read(context.Background(), func(r *Reader) error {
		snapshot, e := r.Snapshot("commitment-1")
		if e == nil && get(get(snapshot, "head"), "mutationVersion") != float64(2) {
			t.Fatal("same root mutation missing")
		}
		return e
	}))
}
func TestCaughtChildFailureKeepsAuthorityGap(t *testing.T) {
	f := seeded(t)
	initial := stringify(t, tableValue(t, f))
	f.fault = "afterWrite"
	must(t, f.store.Transaction(context.Background(), func(parent *Store) error {
		e := parent.Transaction(context.Background(), func(child *Store) error {
			s, _ := NewPrivacyService(child, f.service.owners)
			return s.PurgeSource("scene", "root")
		})
		if e == nil || !strings.Contains(e.Error(), "synthetic_afterWrite") {
			t.Fatalf("child failure %v", e)
		}
		return nil
	}))
	if stringify(t, tableValue(t, f)) != initial {
		t.Fatal("child main writes escaped rollback")
	}
	var n int
	must(t, f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&n))
	if n != 1 {
		t.Fatal("durable intent lost")
	}
	if e := f.store.Read(context.Background(), func(r *Reader) error { _, e := r.Head("commitment-1"); return e }); e == nil {
		t.Fatal("ACK gap allowed publication")
	}
}
func TestDeleteLegacyAndTemporalReaderRequirements(t *testing.T) {
	for _, temporal := range []bool{false, true} {
		f := seeded(t)
		if !temporal {
			_, e := f.main.Exec("DROP TABLE memory_commitment_transitions;DROP TABLE memory_commitment_temporal_windows")
			must(t, e)
		}
		must(t, f.store.Transaction(context.Background(), func(store *Store) error {
			s, _ := NewPrivacyService(store, f.service.owners)
			return s.EraseRetained(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
		}))
		var raw string
		must(t, f.main.QueryRow("SELECT value FROM recovery_gate").Scan(&raw))
		want := float64(3)
		if temporal {
			want = 10
		}
		if get(parse(t, raw), "minimumReader") != want {
			t.Fatalf("reader wanted %v: %s", want, raw)
		}
		must(t, f.external.QueryRow("SELECT payload FROM revocations").Scan(&raw))
		event := parse(t, raw)
		if get(event, "kind") != "commitment_delete" || get(event, "schemaVersion") != float64(1) {
			t.Fatal("wrong deletion event")
		}
	}
}
func TestImmutableTermsAndCapturedProofEquality(t *testing.T) {
	f := seeded(t)
	must(t, f.store.Transaction(context.Background(), func(s *Store) error {
		head, e := s.reader().Head("commitment-1")
		if e != nil {
			return e
		}
		revision, e := s.reader().Revision("commitment-1", 1)
		if e != nil {
			return e
		}
		changed := clone(revision).(*Object)
		_ = object(get(changed, "terms")).Set("title", "replacement")
		if e = s.Put(head, changed); e == nil || e.Error() != "commitment_immutable_terms" {
			t.Fatalf("immutable terms result %v", e)
		}
		return nil
	}))
	before := tableValue(t, f)
	for _, scene := range []string{"other", "scene"} {
		must(t, f.store.Transaction(context.Background(), func(s *Store) error {
			service, _ := NewPrivacyService(s, f.service.owners)
			return service.Requalify(Context{SceneID: scene}, []Transition{{Reference{ID: "root", Version: 1, DeletionEpoch: 0}, Reference{ID: "root", Version: 2, DeletionEpoch: 0}}}, func(_ Context, e *Object) (*Candidate, error) {
				p := clone(arr(get(e, "proofs"))[0]).(*Object)
				_ = object(get(p, "subjectWitness")).Set("mentionPartIndex", float64(0))
				return &Candidate{Reference{ID: "root", Version: 2, DeletionEpoch: 0}, &Assertion{Record: object(parse(t, `{"id":"root","scope":{"kind":"scene","sceneId":"scene","personId":"a"},"content":"Synthetic declaration root","kind":"fact","epistemicKind":"explicit","status":"active"}`)), Proofs: []any{p}}}, nil
			})
		}))
	}
	if !deepEqual(before, tableValue(t, f)) {
		t.Fatal("new proof revived original terms")
	}
}
func TestMalformedTemporalSQLiteTypes(t *testing.T) {
	for _, q := range []string{"UPDATE memory_commitment_temporal_windows SET value=CAST(value AS BLOB)", "UPDATE memory_commitment_transitions SET value=CAST(value AS BLOB)", "UPDATE memory_commitment_transitions SET mutation_version=CAST(mutation_version AS BLOB)", "UPDATE memory_commitment_temporal_windows SET scene_id=CAST(scene_id AS BLOB)", "UPDATE memory_commitment_transitions SET scene_id=CAST(scene_id AS BLOB)"} {
		t.Run(q, func(t *testing.T) {
			f := seeded(t)
			_, e := f.main.Exec(q)
			must(t, e)
			e = f.store.Read(context.Background(), func(r *Reader) error { _, e := r.Timeline("commitment-1"); return e })
			if e == nil {
				t.Fatal("SQLite coercion accepted temporal corruption")
			}
		})
	}
}
func TestReadHandlesExpireAndMutationErrorsDoNotBecomeAbsence(t *testing.T) {
	f := seeded(t)
	var reader *Reader
	must(t, f.store.Read(context.Background(), func(r *Reader) error { reader = r; return nil }))
	if _, e := reader.Head("commitment-1"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped read %v", e)
	}
	_, e := f.main.Exec("DROP TABLE memory_commitment_commands")
	must(t, e)
	must(t, f.store.Read(context.Background(), func(r *Reader) error { _, e := r.Head("commitment-1"); return e }))
	_, e = f.main.Exec("DROP TABLE memory_commitment_heads")
	must(t, e)
	if e = f.store.Read(context.Background(), func(r *Reader) error { _, e := r.Head("commitment-1"); return e }); e == nil {
		t.Fatal("missing source table hidden as empty")
	}
}

func TestOptionalSurvivingDoesNotHideExpiredScope(t *testing.T) {
	f := seeded(t)
	if e := f.service.Requalify(Context{SceneID: "scene"}, nil, nil); e == nil {
		t.Fatal("missing authority hid required write scope")
	}
	var escaped *PrivacyService
	must(t, f.store.Transaction(context.Background(), func(s *Store) error {
		escaped, _ = NewPrivacyService(s, f.service.owners)
		return escaped.Requalify(Context{SceneID: "scene"}, nil, nil)
	}))
	if e := escaped.Requalify(Context{SceneID: "scene"}, nil, nil); e == nil {
		t.Fatal("missing authority hid expired write scope")
	}
}
