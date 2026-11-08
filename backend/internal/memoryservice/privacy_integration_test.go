package memoryservice

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"strings"
	"testing"
)

func privacyCase(t *testing.T, name, operation string) (privacyFixture, manualStep) {
	t.Helper()
	d := readPrivacyFixture(t)
	for _, c := range d.Cases {
		if c.Name != name {
			continue
		}
		for _, step := range c.Steps {
			if step.Operation == operation {
				return d, step
			}
		}
	}
	t.Fatal("missing fixture", name, operation)
	return d, manualStep{}
}
func TestPrivacyCaptureFailureRetainsOnlyRootIntent(t *testing.T) {
	for _, op := range []string{"delete", "recall"} {
		t.Run(op, func(t *testing.T) {
			d, step := privacyCase(t, "semantic-partition-delete", "delete")
			s, f := privacyFixed(t, step.Before)
			privacySeed(t, f, d.Schemas, step.Before)
			if _, e := f.main.Exec("UPDATE memory_semantic_candidates SET value='{' "); e != nil {
				t.Fatal(e)
			}
			before := privacySnapshot(t, f.main)
			head, e := f.recovery.Head(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			var id string
			if e := f.main.QueryRow("SELECT id FROM memories ORDER BY rowid LIMIT 1").Scan(&id); e != nil {
				t.Fatal(e)
			}
			if op == "delete" {
				_, e = s.Delete(context.Background(), id)
			} else {
				_, e = s.Recall(context.Background(), "s", "m1")
			}
			if e == nil {
				t.Fatal("corrupt semantic capture accepted")
			}
			after, e := f.recovery.Head(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if after.Sequence != head.Sequence+1 {
				t.Fatalf("root intent count before=%+v after=%+v", head, after)
			}
			gate, e := recovery.ReadGate(context.Background(), f.main)
			if e != nil {
				t.Fatal(e)
			}
			if gate.Head.Sequence != head.Sequence {
				t.Fatal("failure acknowledged business completion")
			}
			privacyCompare(t, privacySnapshot(t, f.main), before)
		})
	}
}
func TestPrivacyLateSemanticFaultRollsBackAllOwners(t *testing.T) {
	for _, mode := range []string{"ABORT", "ROLLBACK"} {
		t.Run(mode, func(t *testing.T) {
			d, step := privacyCase(t, "semantic-partition-or-recall-requalify", "recall")
			s, f := privacyFixed(t, step.Before)
			privacySeed(t, f, d.Schemas, step.Before)
			before := privacySnapshot(t, f.main)
			if _, e := f.main.Exec("CREATE TRIGGER privacy_late BEFORE UPDATE ON memory_semantic_candidates WHEN NEW.version=3 BEGIN SELECT RAISE(" + mode + ",'privacy_late_semantic'); END"); e != nil {
				t.Fatal(e)
			}
			head, e := f.recovery.Head(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			_, e = s.Recall(context.Background(), "s", "m1")
			if e == nil || !strings.Contains(e.Error(), "privacy_late_semantic") {
				t.Fatalf("late fault not reached: %v", e)
			}
			if mode == "ROLLBACK" && !errors.Is(e, sql.ErrTxDone) {
				t.Fatalf("native loss not latched: %v", e)
			}
			after, e := f.recovery.Head(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if after.Sequence <= head.Sequence {
				t.Fatal("missing durable privacy intents")
			}
			gate, e := recovery.ReadGate(context.Background(), f.main)
			if e != nil {
				t.Fatal(e)
			}
			if gate.Head.Sequence != head.Sequence {
				t.Fatal("late failure ACKed")
			}
			privacyCompare(t, privacySnapshot(t, f.main), before)
			if _, e = s.Recall(context.Background(), "s", "m1"); e == nil {
				t.Fatal("retry bypassed recovery mismatch")
			}
		})
	}
}
func TestPreparedPrivacyConsumedAndExpired(t *testing.T) {
	d, step := privacyCase(t, "recall-or-support-survivor", "recall")
	s, f := privacyFixed(t, step.Before)
	privacySeed(t, f, d.Schemas, step.Before)
	var prepared func() ([]string, error)
	e := f.store.Transaction(context.Background(), func(st *assertions.Store) error {
		return st.WithWriteSession(func(ws *recovery.WriteSession) error {
			var e error
			prepared, e = s.PrepareRecall(context.Background(), ws, "s", "m1")
			if e != nil {
				return e
			}
			if _, e = prepared(); e != nil {
				return e
			}
			if _, e = prepared(); e == nil || e.Error() != "memory_recall_preparation_consumed" {
				t.Fatalf("repeat %v", e)
			}
			return nil
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = prepared(); e == nil || e.Error() != "memory_recall_preparation_consumed" {
		t.Fatalf("post-root repeat %v", e)
	}
	s, f = privacyFixed(t, step.Before)
	privacySeed(t, f, d.Schemas, step.Before)
	e = f.store.Transaction(context.Background(), func(st *assertions.Store) error {
		return st.WithWriteSession(func(ws *recovery.WriteSession) error {
			var e error
			prepared, e = s.PrepareRecall(context.Background(), ws, "s", "m1")
			return e
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = prepared(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired %v", e)
	}
}
func TestRuntimePrivacyBindingCompletesRealRecall(t *testing.T) {
	d, step := privacyCase(t, "recall-or-support-survivor", "recall")
	s, f := privacyFixed(t, step.Before)
	privacySeed(t, f, d.Schemas, step.Before)
	a, e := sourceevents.NewRuntimeAuthority(f.events)
	if e != nil {
		t.Fatal(e)
	}
	a = s.BindRecallAuthority(a)
	scene := botruntime.Scene{}
	scene.ID = "s"
	scene.Kind = "group"
	if e = a.Recall(context.Background(), scene, "m1", nil, nil); e != nil {
		t.Fatal(e)
	}
	privacyCompare(t, privacySnapshot(t, f.main), step.After)
}

func TestCaughtNativePrivacyLossCannotAutocommit(t *testing.T) {
	d, step := privacyCase(t, "semantic-partition-or-recall-requalify", "recall")
	s, f := privacyFixed(t, step.Before)
	privacySeed(t, f, d.Schemas, step.Before)
	if _, e := f.main.Exec("CREATE TRIGGER privacy_native_loss BEFORE UPDATE ON memory_semantic_candidates WHEN NEW.version=3 BEGIN SELECT RAISE(ROLLBACK,'privacy_native_loss'); END"); e != nil {
		t.Fatal(e)
	}
	var executed, post error
	e := f.store.Transaction(context.Background(), func(st *assertions.Store) error {
		return st.WithWriteSession(func(ws *recovery.WriteSession) error {
			prepared, e := s.PrepareRecall(context.Background(), ws, "s", "m1")
			if e != nil {
				return e
			}
			_, executed = prepared()
			_, post = ws.Exec("INSERT INTO config(key,value) VALUES('privacy_no_autocommit','true')")
			return nil
		})
	})
	if executed == nil || !strings.Contains(executed.Error(), "privacy_native_loss") || !errors.Is(executed, sql.ErrTxDone) || !errors.Is(post, sql.ErrTxDone) || !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("executed=%v post=%v outer=%v", executed, post, e)
	}
	var n int
	if e := f.main.QueryRow("SELECT count(*) FROM config WHERE key='privacy_no_autocommit'").Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatal("post-loss autocommit occurred")
	}
}
func TestPrivacyDeleteRemovesActualVectorsAndProjections(t *testing.T) {
	d, step := privacyCase(t, "workspace-projection-delete", "delete")
	s, f := privacyFixed(t, step.Before)
	privacySeed(t, f, d.Schemas, step.Before)
	var id string
	if e := f.main.QueryRow("SELECT id FROM memories LIMIT 1").Scan(&id); e != nil {
		t.Fatal(e)
	}
	if _, e := f.main.Exec("INSERT INTO vector_spaces VALUES('privacy-space','{}')"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.main.Exec("INSERT INTO memory_vectors VALUES(?,?,?)", id, "privacy-space", []byte{1, 2, 3, 4}); e != nil {
		t.Fatal(e)
	}
	deleted, e := s.Delete(context.Background(), id)
	if e != nil || !deleted {
		t.Fatalf("delete %v %v", deleted, e)
	}
	for _, q := range []string{"SELECT count(*) FROM memory_vectors WHERE memory_id=?", "SELECT count(*) FROM memory_fts WHERE assertion_id=?", "SELECT count(*) FROM memory_person_projection_state WHERE assertion_id=?", "SELECT count(*) FROM memory_assertion_revisions WHERE assertion_id=?"} {
		var n int
		if e := f.main.QueryRow(q, id).Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n != 0 {
			t.Fatalf("retained deleted data: %s", q)
		}
	}
}
