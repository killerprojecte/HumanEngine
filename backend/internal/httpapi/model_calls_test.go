package httpapi

import (
	"context"

	"encoding/json"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"net/http/httptest"
	"os"
	"regexp"

	"testing"
)

func TestModelCallsActualSourceHTTP(t *testing.T) {
	raw, err := os.ReadFile("testdata/modelcalls-http-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name, Path, Method, Text         string
			Status                           int
			Empty, Disabled, BadAuth, NoAuth bool
			ResponseHeaders                  map[string]*string
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	normalize := func(s string) string {
		for _, pair := range [][2]string{{`"id":"[^"]+"`, `"id":"<id>"`}, {`"at":"[^"]+"`, `"at":"<time>"`}, {`"requestId":"[^"]+"`, `"requestId":"<request-id>"`}} {
			s = regexp.MustCompile(pair[0]).ReplaceAllString(s, pair[1])
		}
		return s
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			server, db, authority, _, _ := configurationFixture(t)
			server.token = "synthetic-token"
			schemaRaw, err := os.ReadFile("../modelcalls/testdata/source-output.json")
			if err != nil {
				t.Fatal(err)
			}
			var source struct{ Schema string }
			if err = json.Unmarshal(schemaRaw, &source); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(source.Schema); err != nil {
				t.Fatal(err)
			}
			store := modelcalls.New(db)
			publisher, err := recovery.NewReadPublisher(context.Background(), db, authority)
			if err != nil {
				t.Fatal(err)
			}
			defer publisher.CloseAndWait(context.Background())
			if err = server.WithModelCalls(store, publisher); err != nil {
				t.Fatal(err)
			}
			number := func(v float64) *float64 { return &v }
			for i, role := range []string{"thinker", "vision"} {
				event := "model.completed"
				if i == 1 {
					event = "model.failed"
				}
				if err = store.Record(context.Background(), modelcalls.Observation{At: "2099-01-01T00:00:00.000Z", ModelID: "synthetic", ProviderID: "provider", Role: role, Event: event, Attempt: i + 1, DurationMS: 100, TransportMode: "non_stream", Usage: &modelcalls.Usage{Known: true, Input: number(2), Output: number(4)}}); err != nil {
					t.Fatal(err)
				}
			}
			request := httptest.NewRequest(c.Method, c.Path, nil)
			if !c.NoAuth {
				token := "synthetic-token"
				if c.BadAuth {
					token = "bad"
				}
				request.Header.Set("Authorization", "Bearer "+token)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != c.Status || normalize(response.Body.String()) != c.Text {
				t.Fatalf("got%d %s; source%d %s", response.Code, normalize(response.Body.String()), c.Status, c.Text)
			}
			for key, value := range c.ResponseHeaders {
				if value == nil {
					if response.Header().Get(key) != "" {
						t.Errorf("unexpected%s", key)
					}
				} else if response.Header().Get(key) != *value {
					t.Errorf("header%s got%q expected%q", key, response.Header().Get(key), *value)
				}
			}
		})
	}
}
