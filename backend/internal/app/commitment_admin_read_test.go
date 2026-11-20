package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

func TestCommitmentAdminReadActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/commitment-admin-read-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Node   string
		Scenes []json.RawMessage
		Names  map[string]json.RawMessage
		Inputs []struct{ Context, Candidate json.RawMessage }
		Cases  []struct {
			Name, Path, Method                       string
			Auth                                     *bool
			Seed, Historical, Recall, PrivateHeaders bool
			Status                                   int
			Body                                     map[string]any
		}
	}
	if err := json.Unmarshal(raw, &data); err != nil || data.Node != "v24.22.1" {
		t.Fatalf("invalid fixture: %s %v", data.Node, err)
	}

	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	for _, scene := range data.Scenes {
		id := field(memoryPatchObject(t, scene), "id").(string)
		appRequest(t, app, http.MethodPut, "/api/scenes/"+id, scene)
	}
	for key, name := range data.Names {
		if _, err := db.ExecContext(ctx, "INSERT INTO config(key,value) VALUES(?,?)", key, string(name)); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{}
	labels := map[string]string{}
	for _, row := range data.Cases {
		t.Run(row.Name, func(t *testing.T) {
			if row.Seed || row.Historical {
				settings := memoryPatchObject(t, contract.DefaultSettingsInput())
				if err := settings.Set("memoryCommitmentsEnabled", true); err != nil {
					t.Fatal(err)
				}
				if row.Historical {
					if err := settings.Set("memoryHistoricalReadEnabled", true); err != nil {
						t.Fatal(err)
					}
				}
				raw, err := jsjson.Marshal(settings)
				if err != nil {
					t.Fatal(err)
				}
				appRequest(t, app, http.MethodPut, "/api/settings", json.RawMessage(raw))
			}
			if row.Seed {
				for _, input := range data.Inputs {
					result, err := app.Semantic.Memory.Propose(ctx, memoryPatchObject(t, input.Context), memoryPatchObject(t, input.Candidate), memoryservice.ProposalOptions{})
					if err != nil || result.Status != "saved" || result.Commitment == nil || result.Commitment.Status != "saved" {
						t.Fatalf("commitment admission: %+v %+v %v", result, result.Commitment, err)
					}
					id := field(field(result.Commitment.Item, "ref"), "id").(string)
					labels[id] = fmt.Sprintf("@%d", len(ids))
					ids = append(ids, id)
				}
			}
			if row.Recall {
				if _, err := app.Semantic.Memory.Recall(ctx, "scene", "proposal"); err != nil {
					t.Fatal(err)
				}
			}
			path := row.Path
			if strings.Contains(path, "@0") {
				if len(ids) == 0 {
					t.FailNow()
				}
				path = strings.Replace(path, "@0", ids[0], 1)
			}
			method := row.Method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, path, nil)
			token := "synthetic-token"
			if row.Auth != nil && !*row.Auth {
				token = "wrong-token"
			}
			req.Header.Set("Authorization", "Bearer "+token)
			res := httptest.NewRecorder()
			app.ServeHTTP(res, req)
			if res.Code != row.Status || (res.Header().Get("Cache-Control") == "no-store") != row.PrivateHeaders {
				t.Fatalf("got %d %+v %s; want %d private=%v", res.Code, res.Header(), res.Body, row.Status, row.PrivateHeaders)
			}
			if method == http.MethodHead {
				if res.Body.Len() != 0 {
					t.Fatalf("unexpected HEAD body: %s", res.Body)
				}
				return
			}
			var body map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			stamp := func(row map[string]any, key string) {
				if at, ok := row[key].(string); ok {
					if _, err := time.Parse("2006-01-02T15:04:05.000Z", at); err != nil {
						t.Fatal(err)
					}
					row[key] = "@recorded"
				}
			}
			projectRef := func(item map[string]any) {
				ref := item["ref"].(map[string]any)
				if label, ok := labels[ref["id"].(string)]; ok {
					ref["id"] = label
				}
				stamp(item, "recordedAt")
			}
			if body["ref"] != nil {
				projectRef(body)
			}
			for _, key := range []string{"items", "revisions"} {
				items, _ := body[key].([]any)
				for _, raw := range items {
					projectRef(raw.(map[string]any))
				}
			}
			states, _ := body["recordedStates"].([]any)
			for _, raw := range states {
				state := raw.(map[string]any)
				stamp(state, "recordedAt")
				for _, item := range state["states"].([]any) {
					projectRef(item.(map[string]any))
				}
			}
			window, _ := body["window"].(map[string]any)
			stamp(window, "firstRecordedAt")
			if !reflect.DeepEqual(body, row.Body) {
				got, _ := json.Marshal(body)
				want, _ := json.Marshal(row.Body)
				t.Fatalf("got %s\nwant %s", got, want)
			}
		})
	}
}
