package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningruntime"
)

func TestLearningAdminActualSource(t *testing.T) {
	type sourceCase struct {
		Name, Method, Path, Body string
		Authorized               bool
		Status                   int
		Headers                  map[string]string
		Response                 any
	}
	raw, err := os.ReadFile("testdata/learning-admin-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []sourceCase
	if err = json.Unmarshal(raw, &fixtures); err != nil || len(fixtures) != 14 {
		t.Fatalf("learning source fixtures: %d %v", len(fixtures), err)
	}
	options := applicationOptions(t)
	options.Entries = []string{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	app, err := Open(ctx, options)
	if app != nil {
		t.Cleanup(func() {
			if err := app.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}

	check := func(t *testing.T, fixture sourceCase, failure *learningruntime.Failure) {
		t.Helper()
		path := fixture.Path
		if failure != nil {
			path = strings.ReplaceAll(path, "synthetic%2Ffailure", url.PathEscape(failure.ID))
		}
		req := httptest.NewRequest(fixture.Method, path, strings.NewReader(fixture.Body))
		const suppliedID = "12345678-1234-4123-8123-123456789012"
		req.Header.Set("X-Request-ID", suppliedID)
		sourceFixtureAuth(req, fixture.Authorized)
		if fixture.Body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		app.ServeHTTP(response, req)
		if response.Code != fixture.Status {
			t.Fatalf("%s %s: got %d %s, want %d", fixture.Method, path, response.Code, response.Body, fixture.Status)
		}
		for header, want := range fixture.Headers {
			if got := response.Header().Get(header); got != want {
				t.Fatalf("%s: got %q, want %q", header, got, want)
			}
		}
		if id := response.Header().Get("X-Request-ID"); len(id) != 36 || id == suppliedID {
			t.Fatalf("request ID was not newly assigned: %q", id)
		}
		var got any
		if response.Body.Len() > 0 {
			if strings.Contains(response.Header().Get("Content-Type"), "json") {
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
			} else {
				got = response.Body.String()
			}
		}
		if !reflect.DeepEqual(got, fixture.Response) {
			t.Fatalf("got %#v, want %#v", got, fixture.Response)
		}
	}

	for _, fixture := range fixtures[:8] {
		t.Run(fixture.Name, func(t *testing.T) { check(t, fixture, nil) })
	}
	sceneID := "learning-scene"
	appRequest(t, app, "PUT", "/api/scenes/"+sceneID, map[string]any{
		"id": sceneID, "name": "Synthetic", "kind": "group", "adapterId": "synthetic",
		"accountId": "a", "platformSceneId": "s", "learningEnabled": true,
		"participationEnabled": false, "proactiveEnabled": false,
	})
	settings := learningruntime.DefaultSettings()
	settings.BatchMS, settings.MaxWaitMS, settings.MinIntervalMS, settings.RetryAttempts = 1, 1, 0, 1
	if err := app.Learning.Coordinator.SettingsChanged(settings); err != nil {
		t.Fatal(err)
	}
	ingest := func(id string) {
		t.Helper()
		at := time.Now().UTC().Format(time.RFC3339Nano)
		parts, err := jsjson.Parse(`[{"type":"text","text":"synthetic learning source"}]`)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := jsjson.NewObject(
			jsjson.Member{Key: "id", Value: id}, jsjson.Member{Key: "sceneId", Value: sceneID},
			jsjson.Member{Key: "senderId", Value: "synthetic-user"}, jsjson.Member{Key: "origin", Value: "live"},
			jsjson.Member{Key: "status", Value: "available"}, jsjson.Member{Key: "sentAt", Value: at},
			jsjson.Member{Key: "receivedAt", Value: at},
			jsjson.Member{Key: "parts", Value: parts},
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = app.Engine.Runtime.Ingest(ctx, sceneID, msg, false); err != nil {
			t.Fatal(err)
		}
	}
	awaitFailure := func() learningruntime.Failure {
		t.Helper()
		for {
			status := app.Learning.Coordinator.Status()
			if len(status.Failures) == 1 {
				return status.Failures[0]
			}
			select {
			case <-ctx.Done():
				t.Fatalf("no native failed batch: %+v", status)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	ingest("synthetic-source")
	failure := awaitFailure()
	// Source seeded an equivalent paused result without actually running a
	// model, so only its volatile identity, time and real-run totals differ.
	status := fixtures[8].Response.(map[string]any)
	status["accepted"], status["failed"] = float64(1), float64(1)
	entry := status["failures"].([]any)[0].(map[string]any)
	entry["id"], entry["createdAt"] = failure.ID, failure.CreatedAt
	for _, fixture := range fixtures[8:12] {
		t.Run(fixture.Name, func(t *testing.T) { check(t, fixture, &failure) })
	}

	retryFailure := awaitFailure()
	if retryFailure.ID == failure.ID {
		t.Fatal("retry did not consume the original failure")
	}
	if err := app.Engine.Runtime.Recall(ctx, sceneID, "synthetic-source"); err != nil {
		t.Fatal(err)
	}
	if status := app.Learning.Coordinator.Status(); status.Paused != 0 {
		t.Fatalf("recalled source still paused: %+v", status)
	}
	// Retrying the empty batch succeeds, but the recalled source cannot run again.
	t.Run("retry-does-not-replay-recall", func(t *testing.T) {
		check(t, fixtures[11], &retryFailure)
		s := app.Learning.Coordinator.Status()
		if s.Paused+s.Queued+s.Active+s.Retrying+len(s.Failures) != 0 || s.Failed != 2 {
			t.Fatalf("recalled batch reentered native learning: %+v", s)
		}
	})
	ingest("discard-source")
	discard := awaitFailure()
	t.Run("discard", func(t *testing.T) { check(t, fixtures[13], &discard) })
	if len(app.Learning.Coordinator.Status().Failures) != 0 {
		t.Fatal("discard did not remove the native failure")
	}
}
