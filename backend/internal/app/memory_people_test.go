package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

func TestMemoryPeopleActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/memory-people-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Node     string
		Scenes   []json.RawMessage
		Names    map[string]json.RawMessage
		Proposal struct{ Context, Candidate json.RawMessage }
		Seeds    []json.RawMessage
		Times    []string
		Source   struct{ SceneID, MessageID string }
		Cases    []struct {
			Name, Path, Method string
			Authorized         *bool
			Recall, Delete     bool
			Status             int
			PrivateHeaders     bool
			Body               map[string]any
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" {
		t.Fatalf("invalid source fixture: %s: %v", fixture.Node, err)
	}

	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	for _, scene := range fixture.Scenes {
		value := memoryPatchObject(t, scene)
		id := field(value, "id").(string)
		if field(value, "kind") == "simulation" {
			if _, err := db.ExecContext(ctx, "INSERT INTO scenes(id,value) VALUES(?,?)", id, string(scene)); err != nil {
				t.Fatal(err)
			}
		} else {
			appRequest(t, app, http.MethodPut, "/api/scenes/"+id, scene)
		}
	}
	for key, name := range fixture.Names {
		if _, err := db.ExecContext(ctx, "INSERT INTO config(key,value) VALUES(?,?)", key, string(name)); err != nil {
			t.Fatal(err)
		}
	}
	memory := app.Semantic.Memory
	result, err := memory.Propose(ctx, memoryPatchObject(t, fixture.Proposal.Context), memoryPatchObject(t, fixture.Proposal.Candidate), memoryservice.ProposalOptions{})
	if err != nil || result.Status != "saved" {
		t.Fatalf("fixture proposal: %+v %v", result, err)
	}
	ids := []string{field(result.Memory, "id").(string)}
	for _, input := range fixture.Seeds {
		m, err := memory.Create(ctx, memoryPatchObject(t, input))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, field(m, "id").(string))
	}
	labels := map[string]string{}
	for i, id := range ids {
		labels[id] = fmt.Sprintf("@%d", i)
		if _, err := db.ExecContext(ctx, "UPDATE memories SET value=json_set(value, '$.learnedAt', ?) WHERE id=?", fixture.Times[i], id); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Recall {
				if _, err := memory.Recall(ctx, fixture.Source.SceneID, fixture.Source.MessageID); err != nil {
					t.Fatal(err)
				}
			}
			if c.Delete {
				if found, err := memory.Delete(ctx, ids[1]); err != nil || !found {
					t.Fatalf("fixture delete: %v %v", found, err)
				}
			}
			method := c.Method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, c.Path, nil)
			if c.Authorized == nil || *c.Authorized {
				req.Header.Set("Authorization", "Bearer synthetic-token")
			} else {
				// A rejected explicit bearer reaches the inner API used by the source fixture.
				req.Header.Set("Authorization", "Bearer wrong-token")
			}
			out := httptest.NewRecorder()
			app.ServeHTTP(out, req)
			if out.Code != c.Status || (out.Header().Get("Cache-Control") == "no-store") != c.PrivateHeaders {
				t.Fatalf("status/headers: got %d %+v %s, want %d private=%v", out.Code, out.Header(), out.Body, c.Status, c.PrivateHeaders)
			}
			if method == http.MethodHead {
				if out.Body.Len() != 0 {
					t.Fatalf("HEAD wrote body: %s", out.Body)
				}
				return
			}
			var body map[string]any
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			for key, field := range map[string]string{"records": "id", "timeline": "memoryId"} {
				rows, _ := body[key].([]any)
				for _, rawRow := range rows {
					row := rawRow.(map[string]any)
					id := row[field].(string)
					if label, ok := labels[id]; ok {
						row[field] = label
					}
				}
			}
			if !reflect.DeepEqual(body, c.Body) {
				got, _ := json.Marshal(body)
				want, _ := json.Marshal(c.Body)
				t.Fatalf("got %s\nwant %s", got, want)
			}
		})
	}
}
