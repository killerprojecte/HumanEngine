package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
)

func TestMemorySemanticsActualSource(t *testing.T) {
	var fixture struct {
		Node, Timezone string
		AdmittedAt     time.Time
		SourceHashes   map[string]string
		Scenes         []json.RawMessage
		Seeds          []struct {
			Label             string
			Context, Proposal json.RawMessage
		}
		Groups []struct {
			Person string
			IDs    []string
			Picks  [][]int
		}
		Items []json.RawMessage
		Cases []struct {
			Name, Path, Method, CacheControl, NoSniff string
			Status                                    int
			Body                                      json.RawMessage
			Authorized, Enabled                       *bool
			Answer, Expired, Recall                   bool
		}
	}
	raw, err := os.ReadFile("testdata/memory-semantics-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" || fixture.Timezone != "UTC" || len(fixture.Cases) != 19 {
		t.Fatalf("Invalid oracle: %s %s %v", fixture.Node, fixture.Timezone, err)
	}
	for path, want := range fixture.SourceHashes {
		raw, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
			t.Fatal("source drift", path)
		}
	}
	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, wire := range fixture.Scenes {
		scene := memoryPatchObject(t, wire)
		id := field(scene, "id").(string)
		if field(scene, "kind") == "simulation" {
			exec("INSERT INTO scenes(id,value) VALUES(?,?)", id, string(wire))
		} else {
			appRequest(t, app, http.MethodPut, "/api/scenes/"+id, wire)
		}
	}
	setSemantic := func(t *testing.T, enabled, answer bool) {
		t.Helper()
		settings := appRequest(t, app, http.MethodGet, "/api/settings", nil).(*jsjson.Object)
		_ = settings.Set("memorySemanticEnabled", enabled)
		_ = settings.Set("memorySemanticAnswerEnabled", answer)
		wire, err := jsjson.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, http.MethodPut, "/api/settings", json.RawMessage(wire))
	}
	asObject := func(values ...any) *jsjson.Object {
		o := new(jsjson.Object)
		for i := 0; i < len(values); i += 2 {
			_ = o.Set(values[i].(string), values[i+1])
		}
		return o
	}
	memory := app.Semantic.Memory
	refScope := memoryservice.Context{SceneID: "local"}
	views := map[string]*jsjson.Object{}
	selected := map[string][]any{}
	labels := map[string]string{}
	var firstFormer string
	for _, seed := range fixture.Seeds {
		in := memoryPatchObject(t, seed.Proposal)
		result, err := memory.Propose(ctx, memoryPatchObject(t, seed.Context), in, memoryservice.ProposalOptions{})
		if err != nil || result.Status != "saved" {
			t.Fatalf("fixture proposal: %+v %v", result, err)
		}
		id := field(result.Memory, "id").(string)
		person := field(field(result.Memory, "scope"), "personId").(string)
		if person == "former" && firstFormer == "" {
			firstFormer = id
		}
		labels[id] = seed.Label
		refs, err := memory.CaptureParentReferences(ctx, refScope, []*jsjson.Object{result.Memory})
		if err != nil || len(refs) != 1 {
			t.Fatalf("fixture reference: %+v %v", refs, err)
		}
		ref := refs[0]
		view, err := memory.SemanticCurrent(ctx, refScope, ref)
		if err != nil || view == nil {
			t.Fatalf("fixture authority: %v %v", view, err)
		}
		views[id] = view
		selected[person] = append(selected[person], asObject("ref", asObject("id", ref.ID, "version", float64(ref.Version), "deletionEpoch", float64(ref.DeletionEpoch)), "dependencyKey", field(view, "dependencyKey")))
	}
	setSemantic(t, true, false)
	// The test's installation supplies real current parents above. Seed stable
	// candidate IDs and clocks through the actual semantic durable writer, using
	// the captured synthetic parent material only during fixture admission.
	gate, err := recovery.ReadGate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := recovery.OpenWriter(ctx, gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	store, err := semantic.New(db, writer)
	if err != nil {
		t.Fatal(err)
	}
	scope := semantic.Context{SceneID: "local"}
	for _, group := range fixture.Groups {
		parents := selected[group.Person]
		sorted := slices.Clone(parents)
		idOf := func(v any) string { return field(field(v, "ref"), "id").(string) }
		slices.SortFunc(sorted, func(a, b any) int { return strings.Compare(idOf(a), idOf(b)) })
		wire, err := jsjson.Marshal(sorted)
		if err != nil {
			t.Fatal(err)
		}
		partition := fmt.Sprintf("%x", sha256.Sum256(wire))
		for i, pick := range group.Picks {
			id := group.IDs[i]
			ids := []any{}
			for _, index := range pick {
				ids = append(ids, idOf(parents[index]))
			}
			proposal := asObject("kind", "summary_candidate", "selectedParentIds", ids,
				"text", "Synthetic semantic item "+id, "hypothesis", id == "b-00")
			err = writer.WithWriteSession(ctx, db, func(ws *recovery.WriteSession) error {
				bound := store.WithSession(ws)
				authority := semantic.PrivacyAuthority{AssertReady: ws.AssertReady,
					Current: func(c semantic.Context, ref *jsjson.Object) (*jsjson.Object, error) {
						return views[field(ref, "id").(string)], nil
					},
					PartitionKey: func(semantic.Context, any) (string, error) { return partition, nil },
				}
				result, err := bound.Admit(scope, parents, []any{proposal}, nil, authority, semantic.AdmissionPolicy{
					Enabled: true, LearningEnabled: true, Now: func() time.Time { return fixture.AdmittedAt }, ID: func() (string, error) { return id, nil },
				})
				if err != nil {
					return err
				}
				if len(result.Created) != 1 {
					return fmt.Errorf("fixture admission %s: %+v", id, result)
				}
				if id == "b-00" {
					_, err = bound.Review(scope, id, 1, "rejected", true, authority)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	exec("UPDATE memory_assertion_state SET version=version+1 WHERE assertion_id=?", firstFormer)
	requestID := regexp.MustCompile(`^[0-9a-f-]{36}$`)
	learnedAt := regexp.MustCompile(`"learnedAt":"[0-9]{4}-[^"]+Z"`)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Enabled != nil {
				setSemantic(t, *c.Enabled, c.Answer)
			}
			if c.Expired {
				var prev string
				if err := db.QueryRowContext(ctx, "SELECT json_extract(value,'$.applicableUntil') FROM memory_semantic_candidates WHERE id='b-00'").Scan(&prev); err != nil {
					t.Fatal(err)
				}
				exec("UPDATE memory_semantic_candidates SET value=json_set(value,'$.applicableUntil','2000-01-01T00:00:00.000Z') WHERE id='b-00'")
				defer exec("UPDATE memory_semantic_candidates SET value=json_set(value,'$.applicableUntil',?) WHERE id='b-00'", prev)
			}
			if c.Recall {
				if _, err := memory.Recall(ctx, "local", "current-1"); err != nil {
					t.Fatal(err)
				}
			}
			method := c.Method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, c.Path, nil)
			sourceFixtureAuth(req, c.Authorized == nil || *c.Authorized)
			req.Header.Set("X-Request-ID", "synthetic-spoof")
			out := httptest.NewRecorder()
			app.ServeHTTP(out, req)
			h := out.Header()
			if out.Code != c.Status || h.Get("Cache-Control") != c.CacheControl || h.Get("X-Content-Type-Options") != c.NoSniff || !requestID.MatchString(h.Get("X-Request-ID")) {
				t.Fatalf("HTTP got %d %v %s; want %d cache=%q nosniff=%q", out.Code, h, out.Body, c.Status, c.CacheControl, c.NoSniff)
			}
			if method == http.MethodHead {
				if out.Body.Len() != 0 {
					t.Fatalf("HEAD wrote %s", out.Body)
				}
				return
			}
			got := learnedAt.ReplaceAllString(out.Body.String(), `"learnedAt":"@learnedAt"`)
			for id, label := range labels {
				got = strings.ReplaceAll(got, `"`+id+`"`, `"`+label+`"`)
			}
			raw, err := jsjson.ParseBytes(c.Body)
			if err != nil {
				t.Fatal(err)
			}
			want := raw.(*jsjson.Object)
			if compact, ok := want.Get("items"); ok {
				rows := compact.([]any)
				for i, index := range rows {
					rows[i], err = jsjson.ParseBytes(fixture.Items[int(index.(float64))])
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			wire, err := jsjson.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(wire) {
				t.Fatalf("wire mismatch:\ngot %s\nwant %s", got, wire)
			}
		})
	}
}
