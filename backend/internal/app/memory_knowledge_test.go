package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionproposal"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

func TestMemoryKnowledgeActualSource(t *testing.T) {
	var fixture struct {
		Node       string
		Scenes     []json.RawMessage
		Context    json.RawMessage
		Candidate  json.RawMessage
		BackingRef struct {
			ID string
		}
		LearnedAt string
		IDs       []string
		Rows      []struct {
			Name, Action, CacheControl, NoSniff string
			Request                             json.RawMessage
			Status                              int
			Body                                map[string]any
			RequestID                           bool
		}
	}
	raw, err := os.ReadFile("testdata/memory-knowledge-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" || len(fixture.Rows) < 24 {
		t.Fatalf("invalid actual-source knowledge fixture: %s (%v)", fixture.Node, err)
	}
	a, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	for _, scene := range fixture.Scenes {
		value := memoryPatchObject(t, scene)
		id := field(value, "id").(string)
		if field(value, "kind") == "simulation" {
			if _, err := db.ExecContext(ctx, "INSERT INTO scenes(id,value) VALUES(?,?)", id, string(scene)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		appRequest(t, a, http.MethodPut, "/api/scenes/"+id, scene)
	}
	settings := appRequest(t, a, http.MethodGet, "/api/settings", nil).(*jsjson.Object)
	setKnowledge := func(t *testing.T, enabled bool) {
		t.Helper()
		_ = settings.Set("memoryKnowledgeEnabled", enabled)
		_ = settings.Set("memoryKnowledgeAnswerEnabled", !enabled)
		_ = settings.Set("memoryGraphEnabled", !enabled)
		wire, err := jsjson.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, a, http.MethodPut, "/api/settings", json.RawMessage(wire))
	}
	setKnowledge(t, true)
	call := func(input []byte, authorized bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/memory-knowledge/query", bytes.NewReader(input))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "caller-supplied")
		sourceFixtureAuth(req, authorized)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res
	}
	empty := call([]byte(`{"sceneId":"scene","personId":"person","anchor":{"kind":"person","personId":"person"},"predicates":["uses_tool"],"depth":2}`), true)
	var table bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='memory_knowledge_edges')").Scan(&table); err != nil || table || empty.Code != 200 {
		t.Fatalf("fresh read: HTTP=%d table=%t err=%v body=%s", empty.Code, table, err, empty.Body)
	}
	memory := a.Semantic.Memory
	ids := fixture.IDs
	proposed, err := memory.Propose(ctx, memoryPatchObject(t, fixture.Context), memoryPatchObject(t, fixture.Candidate), memoryservice.ProposalOptions{
		KnowledgeEnabled: true,
		Runtime: assertionproposal.Runtime{
			ID:  func() (string, error) { return fixture.BackingRef.ID, nil },
			Now: func() string { return fixture.LearnedAt },
		},
		KnowledgeID: func() string {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})
	if err != nil || proposed.Status != "saved" || proposed.Knowledge == nil || len(proposed.Knowledge.Created) != 1 || len(ids) != 0 {
		t.Fatalf("native proposal: %+v (%v)", proposed, err)
	}
	createdAt, _ := proposed.Knowledge.Created[0].Get("createdAt")
	const sourceCreatedAt = "2026-11-20T00:00:00.000Z"
	trace := regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	for _, c := range fixture.Rows {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Action {
			case "limit":
				if _, err := db.ExecContext(ctx, `
					WITH RECURSIVE extra(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM extra WHERE n<200)
					INSERT INTO memory_knowledge_edges(id,scene_id,assertion_id,admission_key,value)
					SELECT 'extra-'||n,scene_id,assertion_id,'key-'||n,
						json_set(value,'$.id','extra-'||n,'$.object.entity.id','entity-'||n,'$.object.entity.originEdgeId','extra-'||n)
					FROM memory_knowledge_edges,extra WHERE id='edge'
				`); err != nil {
					t.Fatal(err)
				}
			case "disable":
				setKnowledge(t, false)
			case "recall":
				if _, err := db.ExecContext(ctx, "DELETE FROM memory_knowledge_edges WHERE id LIKE 'extra-%'"); err != nil {
					t.Fatal(err)
				}
				setKnowledge(t, true)
				if _, err := memory.Recall(ctx, "scene", "message"); err != nil {
					t.Fatal(err)
				}
			}
			res := call(c.Request, c.Action != "unauthorized")
			if res.Code != c.Status || res.Header().Get("Cache-Control") != c.CacheControl || res.Header().Get("X-Content-Type-Options") != c.NoSniff || !trace.MatchString(res.Header().Get("X-Request-ID")) {
				t.Fatalf("got HTTP %d %v %s; want %d", res.Code, res.Header(), res.Body, c.Status)
			}
			var body map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if id, ok := body["requestId"]; ok != c.RequestID || ok && id != res.Header().Get("X-Request-ID") {
				t.Fatalf("unexpected body trace: %v", id)
			}
			delete(body, "requestId")
			if edges, ok := body["edges"].([]any); ok {
				normalizeKnowledgeCreationTime(t, edges, createdAt, sourceCreatedAt)
				normalizeKnowledgeCreationTime(t, body["supportClosure"].(map[string]any)["edges"].([]any), createdAt, sourceCreatedAt)
			}
			if !reflect.DeepEqual(body, c.Body) {
				got, _ := json.Marshal(body)
				want, _ := json.Marshal(c.Body)
				t.Fatalf("got %s\nwant %s", got, want)
			}
		})
	}
}

func normalizeKnowledgeCreationTime(t *testing.T, edges []any, native any, source string) {
	t.Helper()
	for _, item := range edges {
		edge := item.(map[string]any)
		if edge["createdAt"] != native {
			t.Fatalf("wrong edge creation time %v (want %v)", edge["createdAt"], native)
		}
		edge["createdAt"] = source
	}
}
