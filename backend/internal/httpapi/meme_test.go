package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func memeHTTP(t *testing.T) (*Server, *sql.DB, *memeengine.Service) {
	t.Helper()
	_, db, _, gate := fixtureServer(t)
	ctx := context.Background()
	a, e := recovery.OpenWriter(ctx, gate.Authority)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	_, e = db.Exec(`CREATE TABLE recall_tombstones(scene_id TEXT,message_id TEXT,recalled_at TEXT,PRIMARY KEY(scene_id,message_id));`)
	if e != nil {
		t.Fatal(e)
	}
	collator, e := topiccollation.New("en-US")
	if errors.Is(e, topiccollation.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
		t.Skip("native ICU profile required for complete Meme runtime HTTP")
	}
	if e != nil {
		t.Fatal("pinned native ICU required", e)
	}
	t.Cleanup(func() { collator.Close() })
	dates, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	store, e := assertions.New(db, a, assertions.Options{DateParser: dates})
	if e != nil {
		t.Fatal(e)
	}
	sources, e := sourceevents.New(db, a, sourceevents.Options{})
	if e != nil {
		t.Fatal(e)
	}
	authority, e := sourceevents.NewRuntimeAuthority(sources)
	if e != nil {
		t.Fatal(e)
	}
	owner, e := memeservice.New(ctx, db, a, store, assertionauthority.Options{DateParser: dates, Compare: collator.Compare}, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	habits, e := habitservice.New(ctx, db, a, habitservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	codec, e := imagecodec.New(os.Getenv("HE_IMAGE_LIBRARY"))
	if e != nil {
		t.Fatal(e)
	}
	policy, e := modelservice.New(modelservice.Config{}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	engine, e := memeengine.NewRuntime(db, a, owner, habits, store, sources, codec, &providertransport.NativeModels{Policy: policy, Client: &providertransport.Client{}}, authority, botruntime.DefaultSettings(), dates, collator.Compare, botruntime.Extension{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { engine.Runtime.Close() })
	server, e := New(db, a, fixtureToken, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = server.WithMemes(engine); e != nil {
		t.Fatal(e)
	}
	return server, db, engine
}
func TestActualMemeHTTPSource(t *testing.T) {
	raw, e := os.ReadFile("testdata/meme-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Initial json.RawMessage
		Cases   []struct {
			Name, Method, Path, Text, Token string
			Body                            *string
			JSONType                        bool
			Status                          int
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	if len(fixture.Cases) < 50 {
		t.Fatal("incomplete source matrix")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			server, db, engine := memeHTTP(t)
			if _, e = db.Exec("INSERT INTO meme_assets(id,value) VALUES(?,?)", strings.Repeat("a", 64), string(fixture.Initial)); e != nil {
				t.Fatal(e)
			}
			body := ""
			if c.Body != nil {
				body = *c.Body
			}
			r := httptest.NewRequest(c.Method, c.Path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+c.Token)
			r.Header.Set("X-Request-ID", "<request-id>")
			if c.JSONType {
				r.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			if w.Code != c.Status || w.Body.String() != c.Text {
				if c.Method == "GET" && strings.HasPrefix(c.Path, "/api/memes") {
					o, _ := memeQuery(r.URL.RawQuery)
					t.Log("direct page error:", engine.Memes.PublishPage(context.Background(), o, func([]byte) error { return nil }))
				}
				t.Fatalf("got %d %s; want %d %s", w.Code, w.Body.String(), c.Status, c.Text)
			}
		})
	}
}
