package experienceservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixtureNow int64 = 1793232000000 // 2026-10-29 UTC
var testScope = Context{SceneID: "scene"}

type liveFixture struct {
	main      *sql.DB
	authority *recovery.Authority
	service   *Service
	events    *sourceevents.Store
	path      string
}

func mustExec(t *testing.T, db *sql.DB, q string, a ...any) {
	t.Helper()
	if _, e := db.Exec(q, a...); e != nil {
		t.Fatal(e)
	}
}
func newLiveFixture(t *testing.T, trials bool) liveFixture {
	t.Helper()
	collator, e := topiccollation.New("en-US")
	if errors.Is(e, topiccollation.ErrUnavailable) {
		if os.Getenv("REQUIRE_ICU") != "" {
			t.Fatal("REQUIRE_ICU: pinned native ICU unavailable")
		}
		t.Skip("requires pinned native ICU; no lexical fallback")
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { collator.Close() })
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	live := filepath.Join(root, "live")
	for _, p := range []string{path, live} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	ad, e := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	mustExec(t, ad, `CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	ad.Close()
	file := filepath.Join(live, "main.sqlite")
	if actual, e := recovery.AuthorityPath(file, path); e != nil || actual != path {
		t.Fatalf("independent authority %q %v", actual, e)
	}
	db, e := sql.Open("sqlite3", file)
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id));CREATE TABLE synthetic_preview(scene TEXT,id TEXT,value TEXT,PRIMARY KEY(scene,id));`)
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: path, MinimumReader: 1, State: "ready"}
	mustExec(t, db, "INSERT INTO recovery_gate VALUES(1,?)", encoded(gate))
	mustExec(t, db, "INSERT INTO scenes VALUES('scene',?)", `{"id":"scene","kind":"group","learningEnabled":true,"adapterId":"synthetic","accountId":"account"}`)
	mustExec(t, db, "INSERT INTO config VALUES('settings',?)", fmt.Sprintf(`{"memoryExperienceEnabled":true,"memoryExperienceHintsEnabled":true,"memoryExperienceAutoActivateEnabled":true,"memoryExperienceFeedbackEnabled":%t}`, trials))
	mustExec(t, db, "INSERT INTO config VALUES('memory-experience-feedback-revision','1')")
	for _, id := range []string{"instruction", "help1", "help2", "negative", "other", "intro", "reply1", "reply2"} {
		sender := "alice"
		if contains([]string{"intro", "reply1", "reply2"}, id) {
			sender = "bot"
		}
		mustExec(t, db, "INSERT INTO synthetic_preview VALUES('scene',?,?)", id, fmt.Sprintf(`{"id":%q,"sceneId":"scene","senderId":%q,"origin":"live","status":"available"}`, id, sender))
	}
	a, e := recovery.OpenWriter(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	// This fixture uses actual SQL, not boolean authorization stubs. Its preview
	// table stands in for the runtime cache, all reads use the exact supplied lease.
	ports, e := NativePorts(func(ws *recovery.WriteSession, scene, id string) (out *jsjson.Object, err error) {
		err = guarded(func() {
			rows := (store{ws}).values("SELECT value FROM synthetic_preview WHERE scene=? AND id=?", scene, id)
			if len(rows) > 0 {
				out = take(jsjson.Parse(rows[0])).(*jsjson.Object)
			}
		})
		return
	}, collator.Compare)
	if e != nil {
		t.Fatal(e)
	}
	serial := 0
	ports.Now = func() int64 { return fixtureNow }
	ports.ID = func() string { serial++; return fmt.Sprintf("id%d", serial) }
	s, e := New(context.Background(), db, a, ports)
	if e != nil {
		t.Fatal(e)
	}
	ev, e := sourceevents.New(db, a, sourceevents.Options{ID: func() string { return "source-event" }, Now: func() string { return isoMillis(fixtureNow) }})
	if e != nil {
		t.Fatal(e)
	}
	return liveFixture{db, a, s, ev, path}
}
func feedback(code string) Feedback {
	return Feedback{Source: Source{MessageID: "instruction", SenderID: "alice"}, Target: Target{MessageID: "intro"}, Code: code, ObservedAt: isoMillis(fixtureNow)}
}
func admitted(t *testing.T, f liveFixture) Policy {
	t.Helper()
	r, e := f.service.RecordFeedback(context.Background(), testScope, feedback("request_conclusion_first"))
	if e != nil || r == nil || r.Candidate == nil {
		t.Fatalf("admission %v %v", r, e)
	}
	return *r.Candidate
}
func TestNativePortsRestartAndSourceScope(t *testing.T) {
	f := newLiveFixture(t, false)
	p := admitted(t, f)
	mustExec(t, f.main, "DELETE FROM synthetic_preview")
	file := filepath.Join(filepath.Dir(f.path), "live", "main.sqlite")
	if e := f.main.Close(); e != nil {
		t.Fatal(e)
	}
	if e := f.authority.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := sql.Open("sqlite3", file)
	if e != nil {
		t.Fatal(e)
	}
	reopened.SetMaxOpenConns(1)
	t.Cleanup(func() { reopened.Close() })
	reopenedAuthority, e := recovery.OpenWriter(context.Background(), f.path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { reopenedAuthority.Close() })
	f.main = reopened
	f.authority = reopenedAuthority
	f.events, e = sourceevents.New(reopened, reopenedAuthority, sourceevents.Options{ID: func() string { return "reopened-event" }, Now: func() string { return isoMillis(fixtureNow) }})
	if e != nil {
		t.Fatal(e)
	}
	restored, e := New(context.Background(), f.main, f.authority, f.service.ports)
	if e != nil {
		t.Fatal(e)
	}
	h, e := restored.Hints(context.Background(), testScope, HintQuery{PersonID: "alice"})
	if e != nil || len(h) != 1 || h[0].ID != p.ID {
		t.Fatalf("restart %v %v", h, e)
	}
	mustExec(t, f.main, "UPDATE scenes SET value=json_set(value,'$.accountId','changed')")
	_, e = f.events.Observe(context.Background(), sourceevents.Change{Kind: "ingress", Identity: sourceevents.Identity{SceneID: "scene", AdapterID: "synthetic", AccountID: "account", MessageID: "instruction", SenderID: "alice"}, BodyHash: strings.Repeat("a", 64)})
	if e != nil {
		t.Fatal(e)
	}
	h, e = restored.Hints(context.Background(), testScope, HintQuery{PersonID: "alice"})
	if e != nil || len(h) != 0 {
		t.Fatalf("scope mismatch leaked hint %v %v", h, e)
	}
}
func TestSourceProofAndWritesShareOwner(t *testing.T) {
	f := newLiveFixture(t, false)
	ctx := context.Background()
	var lease *recovery.WriteSession
	e := f.service.TransactionWithSession(ctx, func(s *Service, ws *recovery.WriteSession) error {
		lease = ws
		_, e := ws.Exec("INSERT INTO recall_tombstones VALUES('scene','instruction','synthetic')")
		if e != nil {
			return e
		}
		_, e = s.RecordFeedback(ctx, testScope, feedback("request_conclusion_first"))
		if e == nil || !strings.Contains(e.Error(), "not_authorized") {
			t.Fatalf("uncommitted proof not visible: %v", e)
		}
		_, e = ws.Exec("DELETE FROM recall_tombstones")
		if e != nil {
			return e
		}
		_, e = s.RecordFeedback(ctx, testScope, feedback("request_conclusion_first"))
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = lease.AssertReady(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("retained capability: %v", e)
	}
}
func TestOrdinarySQLErrorCaughtWithinOwnedScope(t *testing.T) {
	f := newLiveFixture(t, false)
	e := f.service.TransactionWithSession(context.Background(), func(s *Service, ws *recovery.WriteSession) error {
		if _, e := ws.Exec("INSERT INTO scenes VALUES('scene','{}')"); e == nil {
			t.Fatal("expected constraint")
		}
		_, e := s.RecordFeedback(context.Background(), testScope, feedback("request_conclusion_first"))
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestIntentSurvivesFailedPolicyWriteWithoutACK(t *testing.T) {
	f := newLiveFixture(t, false)
	mustExec(t, f.main, `CREATE TRIGGER fail_active BEFORE UPDATE ON memory_experience_policies WHEN json_extract(NEW.value,'$.status')='active' BEGIN SELECT RAISE(ABORT,'synthetic policy fault'); END`)
	_, e := f.service.RecordFeedback(context.Background(), testScope, feedback("request_conclusion_first"))
	if e == nil || !strings.Contains(e.Error(), "synthetic policy fault") {
		t.Fatalf("fault %v", e)
	}
	var n int
	if e = f.main.QueryRow("SELECT count(*) FROM memory_experience_outcomes").Scan(&n); e != nil || n != 0 {
		t.Fatalf("rollback %d %v", n, e)
	}
	authority, e := sql.Open("sqlite3", filepath.Join(f.path, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer authority.Close()
	if e = authority.QueryRow("SELECT count(*) FROM revocations").Scan(&n); e != nil || n != 1 {
		t.Fatalf("durable intent %d %v", n, e)
	}
	if _, e = f.service.List(context.Background(), testScope, ListOptions{}); e == nil {
		t.Fatal("unacknowledged authority must block subsequent use")
	}
}
func TestNativeRollbackPoisonsCaughtNestedCall(t *testing.T) {
	f := newLiveFixture(t, false)
	p := admitted(t, f)
	mustExec(t, f.main, `CREATE TRIGGER fail_use BEFORE INSERT ON memory_experience_uses BEGIN SELECT RAISE(ROLLBACK,'synthetic rollback'); END`)
	e := f.service.TransactionWithSession(context.Background(), func(s *Service, ws *recovery.WriteSession) error {
		_, err := s.RecordUse(context.Background(), testScope, Use{TurnID: "turn", PolicyRef: Reference{ID: p.ID, Version: p.Version}, Kind: "exposed"})
		if err == nil {
			t.Fatal("missing lost transaction error")
		}
		if _, err = ws.Exec("INSERT INTO memory_experience_tombstones VALUES('must-not-commit')"); err == nil {
			t.Fatal("expired writer accepted SQL")
		}
		return nil
	})
	if e == nil {
		t.Fatal("caught rollback committed")
	}
}
func TestPinnedSourceServiceOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/service-oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	var oracle struct {
		Node      string
		Scenarios []struct {
			Trials   bool
			Plan     bool
			Withdraw bool
			Steps    []struct {
				Method string
				Args   []json.RawMessage
				Result json.RawMessage
			}
			Mutations []json.RawMessage
		}
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	if oracle.Node != "v24.22.1" {
		t.Fatal(oracle.Node)
	}
	for _, scenario := range oracle.Scenarios {
		t.Run(fmt.Sprintf("trials=%t-plan=%t-withdraw=%t", scenario.Trials, scenario.Plan, scenario.Withdraw), func(t *testing.T) {
			f := newLiveFixture(t, scenario.Trials)
			ctx := context.Background()
			mutations := []any{}
			f.service.ports.BeforeTrialWrite = func(ws *recovery.WriteSession, m recovery.ExperienceTrial) error {
				mutations = append(mutations, m)
				if e := ws.RequireReader(9); e != nil {
					return e
				}
				_, e := ws.AppendIntent(m)
				return e
			}
			for i, step := range scenario.Steps {
				var value any
				var err error
				arg := func(n int, v any) {
					if e := json.Unmarshal(step.Args[n], v); e != nil {
						t.Fatal(e)
					}
				}
				var scope Context
				if step.Method != "revokeSource" && step.Method != "advance" {
					arg(0, &scope)
				}
				switch step.Method {
				case "advance":
					var delta int64
					arg(0, &delta)
					f.service.ports.Now = func() int64 { return fixtureNow + delta }
				case "proposePolicy":
					var proposal Proposal
					var exposed []Reference
					arg(1, &proposal)
					arg(2, &exposed)
					value, err = f.service.ProposePolicy(ctx, scope, proposal, exposed)
				case "recordFeedback":
					var v Feedback
					arg(1, &v)
					value, err = f.service.RecordFeedback(ctx, scope, v)
				case "listOutcomes":
					var limit int
					arg(1, &limit)
					value, err = f.service.ListOutcomes(ctx, scope, limit)
				case "adminList":
					value, err = f.service.AdminList(ctx, scope, ListOptions{})
				case "hints":
					var q struct {
						RequesterID    string `json:"requesterId"`
						PersonID       string `json:"personId"`
						PersonQuestion bool   `json:"personQuestion"`
						PlanQuestion   bool   `json:"planQuestion"`
					}
					arg(1, &q)
					value, err = f.service.Hints(ctx, scope, HintQuery{RequesterID: q.RequesterID, PersonID: q.PersonID, PersonQuestion: q.PersonQuestion, PlanQuestion: q.PlanQuestion})
				case "recordUse":
					var u Use
					arg(1, &u)
					value, err = f.service.RecordUse(ctx, scope, u)
				case "targetUses":
					var id string
					arg(1, &id)
					value, err = f.service.TargetUses(ctx, scope, id)
				case "validate":
					var id string
					var version int64
					arg(1, &id)
					arg(2, &version)
					value, err = f.service.Validate(ctx, scope, id, version)
				case "revokeSource":
					var scene, id string
					arg(0, &scene)
					arg(1, &id)
					err = f.service.RevokeSource(ctx, scene, id)
				default:
					t.Fatal(step.Method)
				}
				got := map[string]any{"value": value}
				if err != nil {
					got = map[string]any{"error": err.Error()}
				}
				var actual, expected any
				if e := json.Unmarshal([]byte(encoded(got)), &actual); e != nil {
					t.Fatal(e)
				}
				if e := json.Unmarshal(step.Result, &expected); e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("step %d %s\ngot %s\nwant %s", i, step.Method, encoded(got), step.Result)
				}
			}
			want := []any{}
			for _, m := range scenario.Mutations {
				var v any
				json.Unmarshal(m, &v)
				want = append(want, v)
			}
			var got []any
			json.Unmarshal([]byte(encoded(mutations)), &got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("trial authority trace\ngot %s\nwant %s", encoded(got), encoded(want))
			}
		})
	}
}

func TestEditedSourceCannotAcquireNewProofForOldFeedback(t *testing.T) {
	f := newLiveFixture(t, true)
	ctx := context.Background()
	identity := sourceevents.Identity{SceneID: "scene", AdapterID: "synthetic", AccountID: "account", MessageID: "instruction", SenderID: "alice"}
	original, e := f.events.Observe(ctx, sourceevents.Change{Kind: "ingress", Identity: identity, BodyHash: strings.Repeat("a", 64)})
	if e != nil {
		t.Fatal(e)
	}
	input := feedback("request_exact_person_lookup")
	if _, e = f.service.RecordFeedback(ctx, testScope, input); e == nil || !strings.Contains(e.Error(), "snapshot_stale") {
		t.Fatalf("registered source needs exact ref: %v", e)
	}
	input.Source.EventRef = original.Ref
	e = f.service.TransactionWithSession(ctx, func(s *Service, ws *recovery.WriteSession) error {
		edited, e := f.events.WithSession(ws).Observe(ctx, sourceevents.Change{Kind: "edit", Identity: identity, BodyHash: strings.Repeat("b", 64), EditID: "edit", Order: sourceevents.Order{Kind: "platform_sequence", Sequence: 1}})
		if e != nil {
			return e
		}
		input.Source.EventRef = edited.Ref
		_, e = s.RecordFeedback(ctx, testScope, input)
		if e == nil || !strings.Contains(e.Error(), "not_authorized") {
			t.Fatalf("edited source relabeled feedback %v", e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.main.QueryRow("SELECT count(*) FROM memory_experience_outcomes").Scan(&n); e != nil || n != 0 {
		t.Fatalf("old feedback acquired new evidence %d %v", n, e)
	}
}
func TestPolicyRevocationErasesTrialsAndIndependentPositiveMaterial(t *testing.T) {
	f := newLiveFixture(t, true)
	ctx := context.Background()
	r, e := f.service.RecordFeedback(ctx, testScope, feedback("request_exact_person_lookup"))
	if e != nil {
		t.Fatal(e)
	}
	p := r.Candidate
	if _, e = f.service.RecordUse(ctx, testScope, Use{TurnID: "turn1", PolicyRef: Reference{ID: p.ID, Version: p.Version}, Kind: "applied", Operation: "memory_person", TargetMessageID: "reply1"}); e != nil {
		t.Fatal(e)
	}
	report := feedback("user_reported_helpful")
	report.Source.MessageID = "help1"
	report.Target.MessageID = "reply1"
	report.PolicyRef = &Reference{ID: p.ID, Version: p.Version}
	if _, e = f.service.RecordFeedback(ctx, testScope, report); e != nil {
		t.Fatal(e)
	}
	if e = f.service.RevokeSource(ctx, "scene", "instruction"); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"memory_experience_policies", "memory_experience_outcomes", "memory_experience_uses", "memory_experience_validations"} {
		var n int
		if e = f.main.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatalf("%s remaining %d %v", table, n, e)
		}
	}
	var value *string
	var version int64
	if e = f.main.QueryRow("SELECT version,value FROM memory_experience_trials WHERE policy=?", p.ID).Scan(&version, &value); e != nil || value != nil || version != 3 {
		t.Fatalf("trial erase receipt %d %v %v", version, value, e)
	}
}
