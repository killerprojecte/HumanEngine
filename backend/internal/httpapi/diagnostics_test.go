package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modeldiagnostics"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"net/http/httptest"
	"os"
	"regexp"
	"sync/atomic"
	"testing"
)

func TestDiagnosticReadActualSourceHTTP(t *testing.T) {
	raw, err := os.ReadFile("testdata/diagnostics-http-source.json")
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
			server, db, authority, secrets, _ := configurationFixture(t)
			server.token = "synthetic-token"
			var enabled atomic.Bool
			enabled.Store(true)
			store, err := modeldiagnostics.New(modeldiagnostics.Ports{Enabled: enabled.Load, Secrets: secrets.RedactionValues})
			if err != nil {
				t.Fatal(err)
			}
			defer store.CloseAndWait(context.Background())
			publisher, err := recovery.NewReadPublisher(context.Background(), db, authority)
			if err != nil {
				t.Fatal(err)
			}
			defer publisher.CloseAndWait(context.Background())
			owner := &diagnosticTestOwner{Store: store, main: db, authority: authority}
			if err = server.WithDiagnostics(owner, publisher); err != nil {
				t.Fatal(err)
			}
			if !c.Empty {
				message, _ := jsjson.Parse(`{"role":"user","content":"synthetic-secret","images":["private-image"]}`)
				output, _ := jsjson.Parse(`{"text":"synthetic-secret","toolCalls":[]}`)
				if err = store.Capture(context.Background(), modeldiagnostics.Entry{ModelID: "synthetic", Role: "thinker", Messages: []*jsjson.Object{message.(*jsjson.Object)}, Output: output}, []string{"synthetic-secret"}); err != nil {
					t.Fatal(err)
				}
			}
			if c.Disabled {
				enabled.Store(false)
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

// This test wrapper supplies ownership identity around the actual diagnostic
// store and real encrypted credential owner; it does not substitute read logic.
type diagnosticTestOwner struct {
	*modeldiagnostics.Store
	main      *sql.DB
	authority *recovery.Authority
}

func (o *diagnosticTestOwner) OwnedBy(main *sql.DB, a *recovery.Authority) bool {
	return o.main == main && o.authority == a
}
