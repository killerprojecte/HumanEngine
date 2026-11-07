package sourceevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

type runtimeOracle struct {
	Scene string
	Cases []struct {
		Name    string
		Enabled bool
		Options struct{ MaxHeadsTotal *int }
		Initial struct{ Settings, Scene string }
		Steps   []struct {
			Op, Result, Error, Messages, Display, Timeline string
			Captures                                       []bool
			Valid, Unrecalled                              bool
		}
	}
	Bodies  []struct{ Message, Hash, Identity, Media string }
	Display struct{ Scene, Identity, Messages, Expected, Ordinary, Prompt string }
}

func runtimeFixtureData(t *testing.T) runtimeOracle {
	t.Helper()
	raw, e := os.ReadFile("testdata/runtime-authority.json")
	if e != nil {
		t.Fatal(e)
	}
	var data runtimeOracle
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	return data
}
func runtimeObject(t *testing.T, raw string) *jsjson.Object {
	t.Helper()
	value, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return value.(*jsjson.Object)
}
func runtimeObjects(t *testing.T, raw string) []*jsjson.Object {
	t.Helper()
	value, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	out := []*jsjson.Object{}
	for _, v := range value.([]any) {
		out = append(out, v.(*jsjson.Object))
	}
	return out
}
func runtimeValues(rows []*jsjson.Object) []any {
	out := make([]any, len(rows))
	for i, v := range rows {
		out[i] = v
	}
	return out
}
func runtimeFixture(t *testing.T, options Options) (fixture, *RuntimeAuthority) {
	t.Helper()
	f := newFixture(t, options)
	_, e := f.main.Exec(`CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id))`)
	if e != nil {
		t.Fatal(e)
	}
	a, e := NewRuntimeAuthority(f.store)
	if e != nil {
		t.Fatal(e)
	}
	return f, a
}
func runtimePut(t *testing.T, db *sql.DB, table, key, raw string) {
	t.Helper()
	column := "key"
	if table == "scenes" {
		column = "id"
	}
	_, e := db.Exec("INSERT OR REPLACE INTO "+table+"("+column+",value) VALUES(?,?)", text(key), raw)
	if e != nil {
		t.Fatal(e)
	}
}

type runtimeTestClock struct{}
type runtimeTestTimer struct{}

func (runtimeTestClock) Now() float64                                 { return 1793145600000 }
func (runtimeTestClock) AfterFunc(float64, func()) modelservice.Timer { return runtimeTestTimer{} }
func (runtimeTestTimer) Stop() bool                                   { return true }
func newSourceRuntime(t *testing.T, a botruntime.Authority) *botruntime.Runtime {
	t.Helper()
	dates, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
	policy, e := replyplan.New(compare, dates)
	if e != nil {
		t.Fatal(e)
	}
	r, e := botruntime.New(botruntime.DefaultSettings(), botruntime.Services{Authority: a, Dates: dates, Policy: policy, Compare: compare, Clock: runtimeTestClock{}, Models: botruntime.ModelFunc(func(context.Context, botruntime.ModelRequest, func(botruntime.Completion, error)) {
		t.Error("unexpected model")
	})})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(r.Close)
	return r
}
func TestRuntimeSourceAuthorityActualSourceTraces(t *testing.T) {
	data := runtimeFixtureData(t)
	for _, c := range data.Cases {
		t.Run(c.Name, func(t *testing.T) {
			f, a := runtimeFixture(t, Options{MaxHeadsTotal: c.Options.MaxHeadsTotal})
			scene := sceneFromValue(runtimeObject(t, data.Scene))
			runtimePut(t, f.main, "config", "settings", c.Initial.Settings)
			runtimePut(t, f.main, "scenes", scene.ID, c.Initial.Scene)
			r := newSourceRuntime(t, a)
			if e := r.RegisterScene(scene); e != nil {
				t.Fatal(e)
			}
			timeline := []any{}
			captures := []*jsjson.Object{}
			r.AddExtension(&botruntime.Extension{Received: func(m *jsjson.Object, _ botruntime.Scene) error {
				timeline = append(timeline, []any{"received", str(m, "id")})
				return nil
			}, Edited: func(before, after *jsjson.Object, _ botruntime.Scene) error {
				var id any
				if before != nil {
					id = str(before, "id")
				}
				timeline = append(timeline, []any{"edited", id, str(after, "id")})
				return nil
			}, PrepareRecallScoped: func(read botruntime.ReadAuthority, s, id string) (func() error, error) {
				recalled, e := read.IsRecalled(scene, id)
				if e != nil {
					return nil, e
				}
				timeline = append(timeline, []any{"prepare", recalled})
				return func() error {
					recalled, e := read.IsRecalled(scene, id)
					timeline = append(timeline, []any{"prepared", recalled})
					return e
				}, nil
			}})
			for step, want := range c.Steps {
				op, e := jsjson.Parse(want.Op)
				if e != nil {
					t.Fatal(e)
				}
				args := op.([]any)
				var operationError error
				status := ""
				switch args[0] {
				case "ingest":
					_, operationError = r.Ingest(context.Background(), scene.ID, args[1].(*jsjson.Object), false)
				case "edit":
					status, operationError = r.Edit(context.Background(), scene.ID, args[1].(*jsjson.Object), args[2].(*jsjson.Object), false)
				case "flag":
					var settings *jsjson.Object
					e = a.read(func(v *View) error {
						var e error
						value, _, readErr := valueRow(v, "SELECT value FROM config WHERE key=?", "settings")
						if readErr == nil {
							settings = value.(*jsjson.Object)
						}
						e = readErr
						return e
					})
					if e != nil {
						t.Fatal(e)
					}
					_ = settings.Set("sourceEventRevisionsEnabled", args[1])
					runtimePut(t, f.main, "config", "settings", stringify(t, settings))
				case "recall":
					operationError = r.Recall(context.Background(), scene.ID, args[1].(string))
				case "capture":
					snapshot, e := r.Snapshot(scene.ID)
					if e != nil {
						t.Fatal(e)
					}
					for _, m := range snapshot.Messages {
						if str(m, "id") == args[1] {
							captures = append(captures, m)
						}
					}
				}
				gotError := ""
				if operationError != nil {
					gotError = operationError.Error()
				}
				if gotError != want.Error {
					t.Fatalf("step %d error %q want %q", step, gotError, want.Error)
				}
				if args[0] == "edit" && want.Error == "" {
					value, _ := jsjson.Parse(want.Result)
					if status != str(value.(*jsjson.Object), "status") {
						t.Fatalf("step %d status %s", step, status)
					}
				}
				snapshot, e := r.Snapshot(scene.ID)
				if e != nil {
					t.Fatal(e)
				}
				if got := stringify(t, runtimeValues(snapshot.Messages)); got != want.Messages {
					t.Fatalf("step %d messages\n%s\nwant\n%s", step, got, want.Messages)
				}
				observed := []bool{}
				for _, m := range captures {
					ok, e := a.ValidateSources(scene, []*jsjson.Object{m}, nil)
					if e != nil {
						t.Fatal(e)
					}
					observed = append(observed, ok)
				}
				if !reflect.DeepEqual(observed, want.Captures) {
					t.Fatalf("step %d captures %v want %v", step, observed, want.Captures)
				}
				valid, e := a.ValidateSources(scene, snapshot.Messages, nil)
				if e != nil || valid != want.Valid {
					t.Fatalf("step %d valid %v %v", step, valid, e)
				}
				unrecalled, e := a.ValidateSources(scene, nil, []string{"message"})
				if e != nil || unrecalled != want.Unrecalled {
					t.Fatalf("step %d deps %v %v", step, unrecalled, e)
				}
				if got := stringify(t, timeline); got != want.Timeline {
					t.Fatalf("step %d timeline %s want %s", step, got, want.Timeline)
				}
				timeline = []any{}
			}
		})
	}
}
func TestRuntimeSourceExactHelpersDisplayAndPrompt(t *testing.T) {
	data := runtimeFixtureData(t)
	scene := sceneFromValue(runtimeObject(t, data.Scene))
	for _, body := range data.Bodies {
		m := runtimeObject(t, body.Message)
		hash, e := RuntimeSourceBodyHash(m)
		if e != nil || hash != body.Hash {
			t.Fatal(hash, e, body.Hash)
		}
		identity, e := RuntimeSourceIdentity(scene, m)
		if e != nil || stringify(t, identity.Value()) != body.Identity {
			t.Fatal(identity, e)
		}
		reference, _ := jsjson.Parse(`"media\ud800"`)
		media, e := RuntimeSourceMediaReference(reference.(string), &Ref{"event-1", 2, 0})
		if e != nil || media != body.Media {
			t.Fatal(media, e)
		}
	}
	f, a := runtimeFixture(t, Options{})
	onebot := runtimeObject(t, data.Display.Scene)
	scene = sceneFromValue(onebot)
	runtimePut(t, f.main, "scenes", scene.ID, data.Display.Scene)
	runtimePut(t, f.main, "config", "identity:"+scene.ID+":speaker", data.Display.Identity)
	messages := runtimeObjects(t, data.Display.Messages)
	for _, test := range []struct {
		local []string
		want  string
	}{{[]string{"first"}, data.Display.Expected}, {nil, data.Display.Ordinary}} {
		out, e := a.ProjectDisplay(scene, messages, test.local)
		if e != nil || stringify(t, runtimeValues(out)) != test.want {
			t.Fatalf("display %s\nwant %s\nerr %v", stringify(t, runtimeValues(out)), test.want, e)
		}
	}
	if stringify(t, runtimeValues(messages)) != data.Display.Messages {
		t.Fatal("projection mutated caller")
	}
	tools := []*jsjson.Object{runtimeObject(t, `{"name":"tool","schema":{"b":2,"a":1}}`)}
	prompt, e := a.PromptAuthority(scene, tools)
	if e != nil || prompt != data.Display.Prompt {
		t.Fatal(prompt, e, data.Display.Prompt)
	}
	runtimePut(t, f.main, "scenes", scene.ID, data.Display.Scene+" ")
	changed, e := a.PromptAuthority(scene, tools)
	if e != nil || changed == prompt {
		t.Fatal("raw row spelling did not invalidate", e)
	}
}
func TestRuntimeRecallScopesAndDurableFailures(t *testing.T) {
	for _, failure := range []string{"", "prepare", "apply", "prepared"} {
		t.Run(failure, func(t *testing.T) {
			f, a := runtimeFixture(t, Options{})
			scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group"}}
			var retained botruntime.ReadAuthority
			applied := false
			want := errors.New("synthetic_" + failure)
			e := a.Recall(context.Background(), scene, "message", func(read botruntime.ReadAuthority) (func() error, error) {
				retained = read
				recalled, e := read.IsRecalled(scene, "message")
				if e != nil || recalled {
					t.Fatalf("prepare prestate %v %v", recalled, e)
				}
				if failure == "prepare" {
					return nil, want
				}
				return func() error {
					recalled, e := read.IsRecalled(scene, "message")
					if e != nil || !recalled {
						t.Fatalf("prepared poststate %v %v", recalled, e)
					}
					if failure == "prepared" {
						return want
					}
					return nil
				}, nil
			}, func() error {
				applied = true
				if e := retained.Ready(); !errors.Is(e, sql.ErrTxDone) {
					t.Fatalf("facade active between callbacks: %v", e)
				}
				_, e := a.IsRecalled(scene, "message")
				if e == nil || !strings.Contains(e.Error(), "head_changed") {
					t.Fatalf("ordinary connection concealed ACK gap %v", e)
				}
				if failure == "apply" {
					return want
				}
				return nil
			})
			if failure == "" && e != nil || failure != "" && !errors.Is(e, want) {
				t.Fatal(e)
			}
			if applied != (failure != "prepare") {
				t.Fatal("apply order")
			}
			if e := retained.Ready(); !errors.Is(e, sql.ErrTxDone) {
				t.Fatal("retained facade", e)
			}
			var count int
			if e := f.main.QueryRow("SELECT count(*) FROM recall_tombstones").Scan(&count); e != nil {
				t.Fatal(e)
			}
			if failure == "" {
				if count != 1 || a.Ready() != nil {
					t.Fatal("successful root missing")
				}
			} else {
				if count != 0 || a.Ready() == nil {
					t.Fatal("failed root was acknowledged")
				}
			}
			if e := f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='source_event_heads'").Scan(&count); e != nil || count != 0 {
				t.Fatal("unregistered recall initialized source schema", count, e)
			}
		})
	}
}
func TestRuntimeSourceReadErrorsAndSnapshotIsolation(t *testing.T) {
	f, a := runtimeFixture(t, Options{})
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group"}}
	if _, e := f.main.Exec("DROP TABLE recall_tombstones"); e != nil {
		t.Fatal(e)
	}
	if _, e := a.IsRecalled(scene, "message"); e == nil {
		t.Fatal("SQL failure became unrecalled")
	}
	m := runtimeObject(t, `{"id":"message","sceneId":"scene","senderId":"speaker","origin":"live","parts":[]}`)
	if _, e := a.ValidateSources(scene, []*jsjson.Object{m}, nil); e == nil {
		t.Fatal("SQL failure became invalid-source false")
	}
	if _, e := a.ProjectDisplay(scene, []*jsjson.Object{m}, nil); e == nil {
		t.Fatal("display concealed SQL failure")
	}
}

func TestRuntimeSnapshotRefValidationOrderAndDeepCopy(t *testing.T) {
	f, a := runtimeFixture(t, Options{})
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group", AdapterID: "synthetic", AccountID: "account"}}
	runtimePut(t, f.main, "scenes", scene.ID, `{"id":"scene","adapterId":"synthetic","accountId":"account","kind":"group"}`)
	data := runtimeFixtureData(t)
	runtimePut(t, f.main, "config", "settings", data.Cases[1].Initial.Settings)
	message := runtimeObject(t, data.Bodies[0].Message)
	invalid := cloneRuntimeJSON(message).(*jsjson.Object)
	_ = invalid.Set("sourceEventRef", obj("invalid", true))
	if valid, e := a.ValidateSources(scene, []*jsjson.Object{invalid}, nil); e != nil || valid {
		t.Fatal("missing head parsed unused ref", valid, e)
	}
	status, trusted, e := a.Observe(context.Background(), scene, message, nil)
	if e != nil || status != "new" {
		t.Fatal(status, e)
	}
	if valid, e := a.ValidateSources(scene, []*jsjson.Object{invalid}, nil); e == nil || valid {
		t.Fatal("matching head ignored invalid ref")
	}
	_ = invalid.Set("parts", []any{})
	if valid, e := a.ValidateSources(scene, []*jsjson.Object{invalid}, nil); e != nil || valid {
		t.Fatal("different body parsed unused ref", valid, e)
	}
	get(message, "parts").([]any)[0].(*jsjson.Object).Set("text", "caller-mutated")
	if valid, e := a.ValidateSources(scene, []*jsjson.Object{trusted}, nil); e != nil || !valid {
		t.Fatal("trusted body retained caller mutable parts", valid, e)
	}
	projected, e := a.ProjectDisplay(scene, []*jsjson.Object{trusted}, nil)
	if e != nil {
		t.Fatal(e)
	}
	get(projected[0], "parts").([]any)[0].(*jsjson.Object).Set("text", "projection-mutated")
	if valid, e := a.ValidateSources(scene, []*jsjson.Object{trusted}, nil); e != nil || !valid {
		t.Fatal("display projection retained mutable parts", valid, e)
	}
}

func TestRuntimeObserveBranchIdentityOrdering(t *testing.T) {
	f, a := runtimeFixture(t, Options{})
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group", AdapterID: "synthetic", AccountID: "account"}}
	if _, e := f.main.Exec("CREATE TABLE source_event_heads(identity_key TEXT)"); e != nil {
		t.Fatal(e)
	}
	m := runtimeObject(t, `{"id":"message","sceneId":"wrong","senderId":"speaker","origin":"live","status":"available","parts":[]}`)
	if _, _, e := a.Observe(context.Background(), scene, m, nil); e == nil || e.Error() != "source_event_scope_mismatch" {
		t.Fatal("ingress read enrollment before scope check", e)
	}
	edit := runtimeObject(t, `{"editId":"edit","order":{"kind":"platform_sequence","sequence":1}}`)
	if _, _, e := a.Observe(context.Background(), scene, m, edit); !errors.Is(e, ErrCorrupt) {
		t.Fatal("edit skipped enrollment check", e)
	}
}
