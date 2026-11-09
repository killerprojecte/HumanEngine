package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

type sceneCommitRecorder struct {
	main  *sql.DB
	calls int
}

func (r *sceneCommitRecorder) SceneChanged(ctx context.Context, scene *jsjson.Object) error {
	id, _ := scene.Get("id")
	var raw string
	if e := r.main.QueryRowContext(ctx, "SELECT value FROM scenes WHERE id=?", id).Scan(&raw); e != nil {
		return e
	}
	encoded, e := jsjson.Stringify(scene)
	if e != nil {
		return e
	}
	if raw != encoded {
		panic("scene effects ran before exact commit")
	}
	r.calls++
	return nil
}
func TestActualSceneConfigurationHTTPSource(t *testing.T) {
	raw, e := os.ReadFile("testdata/scenes-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name, Path, Body, Text string
			Headers                map[string]string
			ResponseHeaders        map[string]*string
			Status                 int
			Rows                   []struct{ ID, Value string }
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			server, db, a, _, _ := configurationFixture(t)
			server.token = "synthetic-scenes-token"
			if _, e := db.Exec("DELETE FROM scenes"); e != nil {
				t.Fatal(e)
			}
			effects := &sceneCommitRecorder{main: db}
			if e := server.WithSceneConfiguration(&scenestore.Store{Main: db, Authority: a}, effects); e != nil {
				t.Fatal(e)
			}
			request := httptest.NewRequest("PUT", c.Path, strings.NewReader(c.Body))
			for k, v := range c.Headers {
				request.Header.Set(k, v)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			text := strings.ReplaceAll(response.Body.String(), c.Headers["X-Request-ID"], "<request-id>")
			if response.Code != c.Status || strings.TrimSpace(text) != c.Text {
				t.Fatalf("status/body got%d %s want%d %s", response.Code, text, c.Status, c.Text)
			}
			for k, v := range c.ResponseHeaders {
				got := response.Header().Get(k)
				if v == nil {
					if got != "" {
						t.Errorf("%s got%q want absent", k, got)
					}
				} else if got != *v {
					t.Errorf("%s got%q want%q", k, got, *v)
				}
			}
			rows, e := db.Query("SELECT id,value FROM scenes ORDER BY id")
			if e != nil {
				t.Fatal(e)
			}
			got := []struct{ ID, Value string }{}
			for rows.Next() {
				var row struct{ ID, Value string }
				if e = rows.Scan(&row.ID, &row.Value); e != nil {
					t.Fatal(e)
				}
				got = append(got, row)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(got, c.Rows) {
				t.Fatalf("rows %v want%v", got, c.Rows)
			}
			want := 0
			if c.Status == 200 {
				want = 1
			}
			if effects.calls != want {
				t.Fatalf("effects=%d want%d", effects.calls, want)
			}
		})
	}
}
