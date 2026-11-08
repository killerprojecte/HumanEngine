package candidateservice

import (
	"context"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func experienceOptions(t *testing.T) NativeExperienceOptions {
	t.Helper()
	compare, e := topiccollation.New("en-US")
	if errors.Is(e, topiccollation.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
		t.Skip("pinned native ICU required")
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { compare.Close() })
	return NativeExperienceOptions{Compare: compare.Compare, AbortRuntime: func(runtime *botruntime.Runtime) { runtime.Close() }, BuildRuntime: func(authority *sourceevents.RuntimeAuthority) (*botruntime.Runtime, error) {
		dates, e := jsdate.New("UTC")
		if e != nil {
			return nil, e
		}
		policy, e := replyplan.New(compare.Compare, dates)
		if e != nil {
			return nil, e
		}
		runtime, e := botruntime.New(botruntime.DefaultSettings(), botruntime.Services{Authority: authority, Compare: compare.Compare, Dates: dates, Policy: policy, Models: botruntime.ModelFunc(func(_ context.Context, _ botruntime.ModelRequest, done func(botruntime.Completion, error)) {
			done(botruntime.Completion{}, errors.New("unexpected model call"))
		})})
		if e == nil {
			t.Cleanup(runtime.Close)
		}
		return runtime, e
	}}
}
func TestExplicitExperienceCandidateOwnsNativeRoutesAndRestart(t *testing.T) {
	old, db, path := fixture(t)
	if e := old.Close(); e != nil {
		t.Fatal(e)
	}
	options := experienceOptions(t)
	s, e := OpenWithMemoryAdministration(context.Background(), path, "test", nil, options)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = db.Exec("CREATE TABLE synthetic_runtime_preview(scene TEXT,id TEXT,value TEXT,PRIMARY KEY(scene,id));CREATE TABLE recall_tombstones(scene_id TEXT,message_id TEXT,recalled_at TEXT,PRIMARY KEY(scene_id,message_id));"); e != nil {
		t.Fatal(e)
	}
	value := settings(t)
	value.Set("memoryExperienceEnabled", true)
	if _, e = s.SetSettings(context.Background(), value); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveScene(context.Background(), scene(t, "Synthetic")); e != nil {
		t.Fatal(e)
	}
	tcp := httptest.NewServer(s)
	defer tcp.Close()
	if code, body := get(t, tcp, "GET", "/api/memory-experience?sceneId=scene"); code != 200 || !strings.Contains(body, `"enabled":true`) {
		t.Fatal(code, body)
	}
	if code, body := get(t, tcp, "GET", "/api/habits"); code != 200 || body != "[]" {
		t.Fatal(code, body)
	}
	raw := `{"id":"p","version":1,"hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sceneId":"scene","action":"verify_plan_qualifiers","condition":{"kind":"plan_question"},"status":"candidate","supportIds":[],"counterexampleIds":[],"generationRoots":[],"generationFamilies":[],"createdAt":"2099-01-01T00:00:00.000Z","expiresAt":"2099-01-07T00:00:00.000Z"}`
	if _, e = db.Exec("INSERT INTO memory_experience_policies VALUES('p','scene',1,?);CREATE TABLE attempts(n);CREATE TRIGGER count_delete AFTER DELETE ON memory_experience_policies BEGIN INSERT INTO attempts VALUES(1);END", raw); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("DELETE", "/api/memory-experience/p", strings.NewReader(`{"sceneId":"scene","expectedVersion":1}`))
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if code, _ := get(t, tcp, "GET", "/api/memory-experience?sceneId=scene"); code != 503 {
		t.Fatal("closed owner admitted", code)
	}
	reopened, e := OpenWithExperience(context.Background(), path, "test", nil, options)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	var count int
	if e = db.QueryRow("SELECT count(*) FROM attempts").Scan(&count); e != nil || count != 1 {
		t.Fatal("restart replayed", count, e)
	}
	preview, e := Open(context.Background(), path, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer preview.Close()
	r = httptest.NewRequest("GET", "/api/memory-experience?sceneId=scene", nil)
	r.Header.Set("Authorization", "Bearer test")
	w = httptest.NewRecorder()
	preview.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("preview enabled native route")
	}
}
func TestExperienceCandidateFailureReleasesWriter(t *testing.T) {
	old, _, path := fixture(t)
	old.Close()
	for i := 0; i < 2; i++ {
		s, e := OpenWithExperience(context.Background(), path, "test", nil, NativeExperienceOptions{})
		if e == nil {
			if s != nil {
				s.Close()
			}
			t.Fatal("missing native ports accepted")
		}
	}
	s, e := OpenWithExperience(context.Background(), path, "test", nil, experienceOptions(t))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
}

func TestExperienceCandidateActualRuntimePreviewAndForeignOwner(t *testing.T) {
	old, db, path := fixture(t)
	old.Close()
	options := experienceOptions(t)
	build := options.BuildRuntime
	var runtime *botruntime.Runtime
	options.BuildRuntime = func(a *sourceevents.RuntimeAuthority) (*botruntime.Runtime, error) {
		var e error
		runtime, e = build(a)
		return runtime, e
	}
	s, e := OpenWithExperience(context.Background(), path, "test", nil, options)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = db.Exec("CREATE TABLE recall_tombstones(scene_id TEXT,message_id TEXT,recalled_at TEXT,PRIMARY KEY(scene_id,message_id))"); e != nil {
		t.Fatal(e)
	}
	value := settings(t)
	value.Set("memoryExperienceEnabled", true)
	if _, e = s.SetSettings(context.Background(), value); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveScene(context.Background(), scene(t, "Synthetic")); e != nil {
		t.Fatal(e)
	}
	live := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group", AdapterID: "adapter", AccountID: "bot", PlatformSceneID: "chat"}, ParticipationEnabled: true, LearningEnabled: true}
	if e = runtime.RegisterScene(live); e != nil {
		t.Fatal(e)
	}
	for _, row := range []struct{ id, sender string }{{"intro", "bot"}, {"instruction", "alice"}} {
		raw := fmt.Sprintf(`{"id":%q,"sceneId":"scene","senderId":%q,"origin":"live","status":"available","sentAt":%q,"receivedAt":%q,"parts":[{"type":"text","text":"Synthetic retained body"}]}`, row.id, row.sender, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
		v, e := jsjson.Parse(raw)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = runtime.Ingest(context.Background(), "scene", v.(*jsjson.Object), false); e != nil {
			t.Fatal(e)
		}
	}
	// One physical connection proves NativeRuntimePorts borrows the writer scope
	// instead of opening a second connection while previewing the retained body.
	s.main.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, e := s.experience.RecordFeedback(ctx, experienceservice.Context{SceneID: "scene"}, experienceservice.Feedback{Source: experienceservice.Source{MessageID: "instruction", SenderID: "alice"}, Target: experienceservice.Target{MessageID: "intro"}, Code: "request_conclusion_first", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if e != nil || result == nil || result.Candidate == nil {
		t.Fatalf("real retained preview: %+v %v", result, e)
	}
	other, _, otherPath := fixture(t)
	other.Close()
	foreign := options
	aborts := 0
	foreign.BuildRuntime = func(*sourceevents.RuntimeAuthority) (*botruntime.Runtime, error) { return runtime, nil }
	foreign.AbortRuntime = func(*botruntime.Runtime) { aborts++ }
	rejected, e := OpenWithExperience(context.Background(), otherPath, "test", nil, foreign)
	if rejected != nil || e == nil || !strings.Contains(e.Error(), "owner_mismatch") || aborts != 1 {
		t.Fatal("foreign runtime accepted", rejected, e, aborts)
	}
}
func TestExperienceCandidateFactoryAndAfterBuildCleanup(t *testing.T) {
	old, db, path := fixture(t)
	old.Close()
	options := experienceOptions(t)
	build := options.BuildRuntime
	aborts := 0
	options.AbortRuntime = func(runtime *botruntime.Runtime) { aborts++; runtime.Close() }
	sentinel := errors.New("factory failure")
	options.BuildRuntime = func(*sourceevents.RuntimeAuthority) (*botruntime.Runtime, error) { return nil, sentinel }
	if s, e := OpenWithExperience(context.Background(), path, "test", nil, options); s != nil || !errors.Is(e, sentinel) || aborts != 0 {
		t.Fatal(s, e, aborts)
	}
	options.BuildRuntime = build
	if _, e := db.Exec("CREATE TABLE ddl_conflict(id);CREATE INDEX memory_experience_policies ON ddl_conflict(id)"); e != nil {
		t.Fatal(e)
	}
	if s, e := OpenWithExperience(context.Background(), path, "test", nil, options); s != nil || e == nil || aborts != 1 {
		t.Fatal("missing after-build cleanup", s, e, aborts)
	}
	if _, e := db.Exec("DROP INDEX memory_experience_policies;DROP TABLE ddl_conflict"); e != nil {
		t.Fatal(e)
	}
	s, e := OpenWithExperience(context.Background(), path, "test", nil, options)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if aborts != 1 {
		t.Fatal("successful runtime lifecycle stolen", aborts)
	}
}
