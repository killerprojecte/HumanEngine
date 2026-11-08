package habitservice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
	"github.com/killerprojecte/HumanEngine/backend/internal/settingsstore"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

const nativeNow = 1793577600000.0

func nativeScene() *Object {
	return obj("id", "s", "kind", "group", "adapterId", "synthetic", "accountId", "account", "platformSceneId", "s", "name", "Synthetic", "participationEnabled", true, "learningEnabled", true, "proactiveEnabled", false)
}
func nativeContext(id, author string) Context {
	return Context{SceneID: "s", SourceEvidence: []any{obj("sceneId", "s", "messageId", id, "senderId", author, "available", true, "isBot", false, "origin", "live", "timestamp", nativeNow, "text", "assistant always use concise replies")}}
}
func nativeInput(id, author string) *Object {
	return obj("kind", "botExpression", "preference", "concise", "direction", float64(1), "sources", []any{obj("sceneId", "s", "messageId", id, "senderId", author)})
}
func nativeService(t *testing.T, f habitFixture, now *float64) *Service {
	t.Helper()
	saveHabitScene(t, f, nativeScene())
	serial := 0
	s, e := New(context.Background(), f.main, f.authority, Options{Now: func(*Service) float64 { return *now }, ID: func() (string, error) { serial++; return fmt.Sprintf("habit-%d", serial), nil }})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func nativeSaved(t *testing.T, s *Service, id, author string) *Object {
	t.Helper()
	r, e := s.Propose(context.Background(), nativeContext(id, author), nativeInput(id, author))
	if e != nil || str(r, "status") != "saved" {
		t.Fatalf("proposal %v %v", r, e)
	}
	return object(get(r, "habit"))
}
func nativeCount(t *testing.T, f habitFixture, table string) int {
	t.Helper()
	var n int
	if e := f.main.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func nativeExec(t *testing.T, f habitFixture, q string, args ...any) {
	t.Helper()
	if _, e := f.main.Exec(q, args...); e != nil {
		t.Fatal(e)
	}
}
func nativeRaw(t *testing.T, f habitFixture) string {
	t.Helper()
	var raw string
	if e := f.main.QueryRow("SELECT value FROM habits ORDER BY rowid LIMIT 1").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	return raw
}

func TestOwnedNestedRollbackAndExpiredCapability(t *testing.T) {
	f := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	ctx := context.Background()
	var escaped *Service
	e := s.Transaction(ctx, func(root *Service) error {
		e := root.Transaction(ctx, func(child *Service) error {
			escaped = child
			nativeSaved(t, child, "one", "p1")
			return errors.New("synthetic_nested")
		})
		if e == nil || !strings.Contains(e.Error(), "synthetic_nested") {
			return fmt.Errorf("nested failure missing: %v", e)
		}
		if _, e := escaped.AdminList(ctx); !errors.Is(e, sql.ErrTxDone) {
			return fmt.Errorf("escaped capability: %v", e)
		}
		nativeSaved(t, root, "two", "p2")
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if nativeCount(t, f, "habits") != 1 {
		t.Fatal("nested insert survived")
	}
	if strings.Contains(nativeRaw(t, f), "p1") {
		t.Fatal("failed child evidence survived")
	}
}
func TestFailureAfterDeleteIntentLeavesMainUnchangedAndRestartClosed(t *testing.T) {
	f := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	h := nativeSaved(t, s, "m", "p")
	before := nativeRaw(t, f)
	nativeExec(t, f, "CREATE TRIGGER synthetic_delete BEFORE DELETE ON habits BEGIN SELECT RAISE(ABORT,'synthetic_delete'); END")
	e := s.Delete(context.Background(), str(h, "id"))
	if e == nil || !strings.Contains(e.Error(), "synthetic_delete") {
		t.Fatal(e)
	}
	if nativeRaw(t, f) != before || nativeCount(t, f, "habit_deleted") != 0 {
		t.Fatal("failed delete changed main")
	}
	head, e := f.authority.Head(context.Background())
	if e != nil || head.Sequence != 1 {
		t.Fatalf("durable deletion intent absent: %+v %v", head, e)
	}
	if _, e := s.List(context.Background(), Context{SceneID: "s"}); e == nil {
		t.Fatal("head gap allowed publication")
	}
	if _, e := New(context.Background(), f.main, f.authority, Options{}); e == nil {
		t.Fatal("restart silently repaired durable intent")
	}
}
func TestRecallFailureKeepsIntentAndRollsBackAllRetractions(t *testing.T) {
	f := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	nativeSaved(t, s, "m", "p")
	before := nativeRaw(t, f)
	nativeExec(t, f, "CREATE TRIGGER synthetic_recall BEFORE UPDATE ON habits BEGIN SELECT RAISE(ABORT,'synthetic_recall'); END")
	_, e := s.Recall(context.Background(), "s", "m")
	if e == nil || !strings.Contains(e.Error(), "synthetic_recall") {
		t.Fatal(e)
	}
	if nativeRaw(t, f) != before || nativeCount(t, f, "recall_tombstones") != 0 {
		t.Fatal("partial recall committed")
	}
	head, e := f.authority.Head(context.Background())
	if e != nil || head.Sequence != 1 {
		t.Fatalf("root intent missing: %+v %v", head, e)
	}
}
func TestNativeRollbackInvalidatesCaughtRoot(t *testing.T) {
	f := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	nativeExec(t, f, "CREATE TRIGGER synthetic_rollback BEFORE INSERT ON habits BEGIN SELECT RAISE(ROLLBACK,'synthetic_rollback'); END")
	e := s.Transaction(context.Background(), func(root *Service) error {
		if _, e := root.Propose(context.Background(), nativeContext("m", "p"), nativeInput("m", "p")); e == nil {
			return errors.New("rollback not reported")
		}
		if _, e := root.session.Exec("INSERT INTO habit_deleted VALUES('forbidden')"); !errors.Is(e, sql.ErrTxDone) {
			return fmt.Errorf("native loss did not revoke capability: %v", e)
		}
		return nil
	})
	if e == nil {
		t.Fatal("caught native rollback escaped owner")
	}
	if nativeCount(t, f, "habits") != 0 || nativeCount(t, f, "habit_deleted") != 0 {
		t.Fatal("autocommit after rollback")
	}
}
func TestExplicitSourceRefEditQualificationAndOwnedSourceComposition(t *testing.T) {
	f := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	ctx := context.Background()
	identity := sourceevents.Identity{SceneID: "s", AdapterID: "synthetic", AccountID: "account", MessageID: "m", SenderID: "p"}
	e := s.Transaction(ctx, func(b *Service) error {
		events := f.events.WithSession(b.session)
		result, e := events.Observe(ctx, sourceevents.Change{Kind: "ingress", Identity: identity, BodyHash: strings.Repeat("a", 64)})
		if e != nil {
			return e
		}
		input := nativeInput("m", "p")
		_ = object(arr(get(input, "sources"))[0]).Set("eventRef", result.Ref.Value())
		out, e := b.Propose(ctx, nativeContext("m", "p"), input)
		if e != nil {
			return e
		}
		if str(out, "status") != "saved" {
			return errors.New("explicit ref rejected")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	before := nativeRaw(t, f)
	_, e = f.events.Observe(ctx, sourceevents.Change{Kind: "edit", Identity: identity, BodyHash: strings.Repeat("b", 64), EditID: "edit-1", Order: sourceevents.Order{Kind: "platform_sequence", Sequence: 1}})
	if e != nil {
		t.Fatal(e)
	}
	listed, e := s.List(ctx, Context{SceneID: "s"})
	if e != nil || len(listed) != 0 {
		t.Fatalf("edited ref still usable: %v %v", listed, e)
	}
	if nativeRaw(t, f) != before {
		t.Fatal("current qualification erased historical evidence")
	}
	id, e := s.ValidationIdentity(ctx, Context{SceneID: "s"}, "habit-1")
	if e != nil || id != nil {
		t.Fatal("edited evidence retained identity", e)
	}
}

// This proves the habit adapter only, not the complete engine recall chain.
// Production must retain experience -> memory -> memes -> habits -> learning.
func TestSceneSettingsAndRuntimeRecallComposition(t *testing.T) {
	f := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	ctx := context.Background()
	nativeSaved(t, s, "m", "p")
	settings := settingsstore.Store{Main: f.main, Authority: f.authority}
	if _, e := settings.Set(ctx, obj("sourceEventRevisionsEnabled", false)); e != nil {
		t.Fatal(e)
	}
	// There is no global habit switch in the source domain. A settings write must
	// not invent one; current scene learningEnabled remains the admission policy.
	nativeSaved(t, s, "m2", "p2")
	scene := nativeScene()
	_ = scene.Set("learningEnabled", false)
	sceneStore := scenestore.Store{Main: f.main, Authority: f.authority}
	if _, e := sceneStore.Save(ctx, scene); e != nil {
		t.Fatal(e)
	}
	r, e := s.Propose(ctx, nativeContext("m3", "p3"), nativeInput("m3", "p3"))
	if e != nil || str(r, "reason") != "learning_disabled" {
		t.Fatalf("persisted learning switch ignored: %v %v", r, e)
	}
	runtime, e := sourceevents.NewRuntimeAuthority(f.events)
	if e != nil {
		t.Fatal(e)
	}
	runtime = runtime.WithRecallPreparation(s.RuntimeRecallPreparation())
	if e = runtime.Recall(ctx, botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s", Kind: "group", AdapterID: "synthetic", AccountID: "account", PlatformSceneID: "s"}}, "m", nil, nil); e != nil {
		t.Fatal(e)
	}
	records, e := s.AdminList(ctx)
	if e != nil || len(records) != 1 || len(arr(get(records[0], "sources"))) != 1 {
		t.Fatal("runtime did not retract habit in root recall", e)
	}
}
func TestReopenPreservesRawEvidenceAndClockAge(t *testing.T) {
	f := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	nativeSaved(t, s, "m", "p")
	raw := nativeRaw(t, f)
	var file string
	if e := f.main.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&file); e != nil {
		t.Fatal(e)
	}
	if e := f.main.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := sql.Open("sqlite3", file)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	now += 30 * day
	restart, e := New(context.Background(), reopened, f.authority, Options{Now: func(*Service) float64 { return now }})
	if e != nil {
		t.Fatal(e)
	}
	records, e := restart.AdminList(context.Background())
	if e != nil || len(records) != 1 || math.Abs(number(records[0], "weight")-.025) > 1e-14 {
		t.Fatalf("restart refreshed age: %v %v", records, e)
	}
	var after string
	if e = reopened.QueryRow("SELECT value FROM habits").Scan(&after); e != nil || raw != after {
		t.Fatal("restart changed raw evidence", e)
	}
}
func TestForeignSessionAndNumericDirectionBoundary(t *testing.T) {
	f := newHabitFixture(t)
	other := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	e := other.authority.WithWriteSession(context.Background(), other.main, func(ws *recovery.WriteSession) error {
		_, e := s.WithSession(ws).Propose(context.Background(), nativeContext("m", "p"), nativeInput("m", "p"))
		if e == nil {
			return errors.New("foreign session accepted")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	input := nativeInput("m", "p")
	_ = input.Set("direction", true)
	r, e := s.Propose(context.Background(), nativeContext("m", "p"), input)
	if e != nil || str(r, "reason") != "invalid_candidate" {
		t.Fatal("boolean direction accepted", e)
	}
}
func TestNoCallerAliasCanMutatePersistedOrReturnedEvidence(t *testing.T) {
	f := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	input := nativeInput("m", "p")
	_ = input.Set("kind", "sceneRule")
	_ = input.Set("content", "Synthetic local rule")
	r, e := s.Propose(context.Background(), nativeContext("m", "p"), input)
	if e != nil {
		t.Fatal(e)
	}
	raw := nativeRaw(t, f)
	_ = object(arr(get(input, "sources"))[0]).Set("senderId", "changed")
	_ = object(arr(get(object(get(r, "habit")), "sources"))[0]).Set("senderId", "changed-again")
	if nativeRaw(t, f) != raw {
		t.Fatal("caller alias changed persisted JSON")
	}
	visible, e := s.List(context.Background(), Context{SceneID: "other"})
	if e != nil || len(visible) != 0 {
		t.Fatal("scene privacy failure", e)
	}
	if _, e = jsjson.Stringify(r); e != nil {
		t.Fatal(e)
	}
}

func TestConcurrentAuthorVotesSerializeWithoutLostEvidence(t *testing.T) {
	f := newHabitFixture(t)
	now := nativeNow
	s := nativeService(t, f, &now)
	var workers sync.WaitGroup
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			id := fmt.Sprintf("m-%d", i)
			p := fmt.Sprintf("p-%d", i)
			r, e := s.Propose(context.Background(), nativeContext(id, p), nativeInput(id, p))
			if e != nil {
				failures <- e
			} else if str(r, "status") != "saved" {
				failures <- fmt.Errorf("proposal %d rejected", i)
			}
		}(i)
	}
	workers.Wait()
	close(failures)
	for e := range failures {
		t.Error(e)
	}
	records, e := s.AdminList(context.Background())
	if e != nil || len(records) != 1 || len(arr(get(records[0], "sources"))) != 20 {
		t.Fatalf("lost author votes: %v %v", records, e)
	}
}
