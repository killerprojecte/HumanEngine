package botruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Persistence/projection are independently covered by sourceevents' real SQLite
// differential test. This boundary double exposes only routing, recall and observe.
type ingressAuthority struct {
	*testAuthority
	route   func(Scene, *Object) error
	recall  func(Scene, string) (bool, error)
	observe func(Scene, *Object)
}

func (a *ingressAuthority) RouteIngress(_ context.Context, s Scene, m *Object) error {
	return a.route(s, m)
}
func (a *ingressAuthority) IsRecalled(s Scene, id string) (bool, error) { return a.recall(s, id) }
func (a *ingressAuthority) Observe(ctx context.Context, s Scene, m, e *Object) (string, *Object, error) {
	if a.observe != nil {
		a.observe(s, m)
	}
	return a.testAuthority.Observe(ctx, s, m, e)
}

func TestIngestSceneActualSourceBoundary(t *testing.T) {
	data, err := os.ReadFile("../sourceevents/testdata/ingress-routing.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Spec struct {
				Name, Failure, RawIdentity string
				Repeat, Closed, Recalled   bool
				Admission                  *bool
			}
			Input    struct{ Scene, Message string }
			Expected struct {
				Error       *string
				Timeline    []any
				Registered  bool
				StateExists *bool
				Messages    []any
			}
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Spec.Name, func(t *testing.T) {
			r, base, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Error("unexpected model") }))
			cfg := r.config()
			cfg.TopicAdmissionEnabled = c.Spec.Admission == nil || *c.Spec.Admission
			r.settings.Store(&cfg)
			var scene Scene
			if err := json.Unmarshal([]byte(c.Input.Scene), &scene.SceneRef); err != nil {
				t.Fatal(err)
			}
			value, err := jsjson.Parse(c.Input.Message)
			if err != nil {
				t.Fatal(err)
			}
			m := value.(*Object)
			timeline := []any{}
			routeFailure := c.Spec.Failure == "saveScene" || c.Spec.Failure == "observeIdentity" || c.Spec.Name == "raw-null-identity-fallback"
			a := &ingressAuthority{testAuthority: base}
			a.route = func(s Scene, body *Object) error {
				timeline = append(timeline, "route")
				registered, ok := r.RegisteredScene(s.ID)
				if !ok || registered.PlatformSceneID != scene.PlatformSceneID {
					t.Error("scene not registered before routing")
				}
				if routeFailure {
					return errors.New(*c.Expected.Error)
				}
				return nil
			}
			a.recall = func(Scene, string) (bool, error) {
				timeline = append(timeline, "recalled")
				if c.Spec.Failure == "recalled" {
					return false, errors.New("synthetic_recalled")
				}
				return c.Spec.Recalled, nil
			}
			r.services.Authority = a
			if c.Spec.Repeat {
				prior := m.Clone()
				_ = prior.Set("senderDisplayName", "Synthetic user")
				if _, err := r.IngestScene(context.Background(), scene, prior, false); err != nil {
					t.Fatal(err)
				}
				if c.Spec.Closed {
					r.state(scene.ID).closed.Store(true)
				}
			}
			timeline = []any{}
			r.AddExtension(&Extension{Received: func(*Object, Scene) error {
				topics := []any{}
				s := r.state(scene.ID)
				s.mu.Lock()
				for _, e := range s.topics.Entries() {
					topics = append(topics, map[string]any{"messageIds": append([]string{}, e.Value.messageIDs...), "version": e.Value.version.Load()})
				}
				s.mu.Unlock()
				timeline = append(timeline, map[string]any{"kind": "received", "topics": topics})
				return nil
			}})
			_, err = r.IngestScene(context.Background(), scene, m, false)
			var actualError *string
			if err != nil {
				v := err.Error()
				actualError = &v
			}
			if !reflect.DeepEqual(actualError, c.Expected.Error) {
				t.Fatalf("error=%v want=%v", err, c.Expected.Error)
			}
			want := []any{}
			if c.Expected.Registered && scene.Kind != "simulation" {
				want = append(want, "route")
			}
			for _, effect := range c.Expected.Timeline {
				if effect == "saveScene" || effect == "observeIdentity" {
					continue
				}
				want = append(want, effect)
			}
			if !reflect.DeepEqual(decoded(timeline), decoded(want)) {
				t.Fatalf("timeline=%v want=%v", timeline, want)
			}
			state := r.state(scene.ID)
			_, registered := r.RegisteredScene(scene.ID)
			if registered != c.Expected.Registered {
				t.Fatalf("registered=%v want=%v", registered, c.Expected.Registered)
			}
			if c.Expected.StateExists != nil && (state != nil) != *c.Expected.StateExists {
				t.Fatalf("stateExists=%v want=%v", state != nil, *c.Expected.StateExists)
			}
			if routeFailure && state != nil {
				t.Fatal("failed first routing created runtime state")
			}
			messages := []any{}
			if state != nil {
				state.mu.Lock()
				for _, message := range state.messages {
					messages = append(messages, message)
				}
				state.mu.Unlock()
			}
			rawMessages, err := jsjson.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			var actualMessages []any
			if err = json.Unmarshal(rawMessages, &actualMessages); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actualMessages, c.Expected.Messages) {
				t.Fatalf("messages=%v want=%v", actualMessages, c.Expected.Messages)
			}
		})
	}
}

func TestIngestSceneNormalizesBeforeRoutingAndRetainsRegistrationOnFailure(t *testing.T) {
	r, base, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
	scene := fixtureScene(false)
	r.AddAdapter(scene.AdapterID, &Adapter{BotUserID: "platform-bot"})
	m := fixtureMessage(scene.ID, "source", "Synthetic")
	_ = m.Set("senderId", "platform-bot")
	_ = m.Set("parts", []any{obj("type", "mention", "userId", "platform-bot")})
	calls := 0
	r.services.Authority = &ingressAuthority{testAuthority: base, route: func(s Scene, m *Object) error {
		calls++
		if get(m, "senderId") != "bot" || get(array(get(m, "parts"))[0], "userId") != "bot" {
			t.Error("normalization must precede routing")
		}
		return errors.New("synthetic_route")
	}, recall: func(Scene, string) (bool, error) { t.Fatal("recall after routing failure"); return false, nil }}
	base.readyErr = errors.New("must_not_call_ready_before_registration")
	if _, err := r.IngestScene(context.Background(), scene, m, false); err == nil || err.Error() != "synthetic_route" {
		t.Fatalf("error=%v", err)
	}
	_, registered := r.RegisteredScene(scene.ID)
	if calls != 1 || !registered || r.state(scene.ID) != nil {
		t.Fatal("routing/registration/state allocation mismatch")
	}
	if get(m, "senderId") != "platform-bot" {
		t.Fatal("caller mutated")
	}
}

func TestReceivedFailureLoggedAndLaterCallbacksContinue(t *testing.T) {
	for _, admission := range []bool{true, false} {
		t.Run(map[bool]string{true: "admission", false: "legacy"}[admission], func(t *testing.T) {
			r, _, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
			cfg := r.config()
			cfg.TopicAdmissionEnabled = admission
			r.settings.Store(&cfg)
			logs, calls := 0, 0
			r.services.Trace = func(e Event) {
				if e.Code == "receive_failed" && e.Level == "error" && e.Module == "extension" {
					logs++
				}
			}
			r.AddExtension(&Extension{Received: func(*Object, Scene) error { return errors.New("synthetic_received") }})
			r.AddExtension(&Extension{Received: func(*Object, Scene) error { calls++; return nil }})
			scene := fixtureScene(false)
			if _, err := r.IngestScene(context.Background(), scene, fixtureMessage(scene.ID, "source", "Synthetic"), false); err != nil {
				t.Fatal(err)
			}
			if logs != 1 || calls != 1 {
				t.Fatalf("logs=%d calls=%d", logs, calls)
			}
		})
	}
}

func TestIngressMetadataRefreshDoesNotCancelScene(t *testing.T) {
	r, _, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
	scene := fixtureScene(false)
	if err := r.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	s := r.state(scene.ID)
	s.mu.Lock()
	s.pending["synthetic-pending"] = true
	s.mu.Unlock()
	scene.ParticipationEnabled = false
	name := "Updated transport name"
	scene.DisplayName = &name
	if _, err := r.IngestScene(context.Background(), scene, fixtureMessage(scene.ID, "source", "Synthetic"), false); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending["synthetic-pending"] {
		t.Fatal("transport metadata refresh cancelled scene")
	}
	if s.scene.DisplayName == nil || *s.scene.DisplayName != name {
		t.Fatal("transport metadata not refreshed")
	}
	if !s.scene.ParticipationEnabled {
		t.Fatal("transport reference overwrote persisted feature switch")
	}
}

func TestLegacyReceivedSeesBodiesBeforeCacheClear(t *testing.T) {
	r, _, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
	cfg := r.config()
	cfg.TopicAdmissionEnabled = false
	cfg.CacheEnabled = false
	r.settings.Store(&cfg)
	scene := fixtureScene(false)
	r.AddExtension(&Extension{Received: func(*Object, Scene) error {
		s := r.state(scene.ID)
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.messages) != 1 || s.topics.Len() != 1 {
			t.Fatal("cache cleared before received callback")
		}
		return nil
	}})
	if _, err := r.IngestScene(context.Background(), scene, fixtureMessage(scene.ID, "source", "Synthetic"), false); err != nil {
		t.Fatal(err)
	}
	s := r.state(scene.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) != 0 {
		t.Fatal("cache not cleared after received callback")
	}
}
