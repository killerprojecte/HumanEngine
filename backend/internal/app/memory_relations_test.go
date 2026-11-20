package app

import (
	"bytes"
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

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

func TestMemoryRelationsActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/memory-relations-source.json")
	if err != nil {
		t.Fatal(err)
	}
	type reply struct {
		Status                int
		Body                  json.RawMessage
		CacheControl, NoSniff string
	}
	var fixture struct {
		Node, Timezone string
		SourceHashes   map[string]string
		Scenes         []json.RawMessage
		Names          map[string]json.RawMessage
		Proposals      []struct {
			Context, Candidate json.RawMessage
			IDs                []string
			Auto               bool
		}
		Times map[string]string
		Cases []struct {
			Name, Path, Method, CacheControl, NoSniff string
			Authorized, Enabled, Hide                 *bool
			Recall, Delete                            bool
			Status                                    int
			Body                                      json.RawMessage
		}
		ReviewCases []struct {
			Name, URL                string
			Body, ContentType        *string
			Authorized, GraphEnabled *bool
			Concurrent               bool
			Result                   []reply
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" || fixture.Timezone != "UTC" {
		t.Fatalf("invalid source fixture: %s %s %v", fixture.Node, fixture.Timezone, err)
	}
	for path, want := range fixture.SourceHashes {
		raw, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
			t.Fatalf("fixture source drift: %s", path)
		}
	}

	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, scene := range fixture.Scenes {
		value := memoryPatchObject(t, scene)
		id := field(value, "id").(string)
		if field(value, "kind") == "simulation" {
			exec("INSERT INTO scenes(id,value) VALUES(?,?)", id, string(scene))
		} else {
			appRequest(t, app, http.MethodPut, "/api/scenes/"+id, scene)
		}
	}
	for key, value := range fixture.Names {
		exec("INSERT INTO config(key,value) VALUES(?,?)", key, string(value))
	}
	setGraph := func(t *testing.T, enabled bool) {
		t.Helper()
		settings := appRequest(t, app, http.MethodGet, "/api/settings", nil).(*jsjson.Object)
		_ = settings.Set("memoryGraphEnabled", enabled)
		wire, err := jsjson.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, http.MethodPut, "/api/settings", json.RawMessage(wire))
	}

	memory := app.Semantic.Memory
	ids := make([]string, 0, len(fixture.Proposals))
	for _, proposal := range fixture.Proposals {
		idsToUse := proposal.IDs
		result, err := memory.Propose(ctx, memoryPatchObject(t, proposal.Context), memoryPatchObject(t, proposal.Candidate), memoryservice.ProposalOptions{
			GraphEnabled: true, GraphAutoAdmission: proposal.Auto,
			RelationID: func() string {
				id := idsToUse[0]
				idsToUse = idsToUse[1:]
				return id
			},
		})
		if err != nil || result.Status != "saved" || result.Relations == nil || len(result.Relations.Created) != 1 || len(idsToUse) != 0 {
			t.Fatalf("fixture proposal: %+v %v", result, err)
		}
		ids = append(ids, field(result.Memory, "id").(string))
	}
	// Mirror the TS review prestate. This Go slice does not expose review.
	exec(`UPDATE memory_relation_candidates SET version=2,value=json_set(value,'$.version',2,'$.status','rejected','$.admission','reviewed','$.reviewedAt',?,'$.actorId','admin') WHERE id='relation-2'`, fixture.Times["reviewedAt"])
	setGraph(t, true)

	timestamps := regexp.MustCompile(`"(createdAt|reviewedAt)":"[0-9]{4}-[0-9]{2}-[0-9]{2}T[^"]+Z"`)
	fieldPattern := regexp.MustCompile(`^"([^"]+)"`)
	reqIDPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Enabled != nil {
				setGraph(t, *c.Enabled)
			}
			if c.Hide != nil {
				if *c.Hide {
					exec("UPDATE memories SET value=json_set(value,'$.internalUse','knowledge_declaration') WHERE id=?", ids[0])
				} else {
					exec("UPDATE memories SET value=json_remove(value,'$.internalUse') WHERE id=?", ids[0])
				}
			}
			if c.Recall {
				if _, err := memory.Recall(ctx, "local", "message-1"); err != nil {
					t.Fatal(err)
				}
			}
			if c.Delete {
				if found, err := memory.Delete(ctx, ids[0]); err != nil || !found {
					t.Fatalf("fixture deletion: %t %v", found, err)
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
			header := out.Header()
			if out.Code != c.Status || header.Get("Cache-Control") != c.CacheControl || header.Get("X-Content-Type-Options") != c.NoSniff ||
				!reqIDPattern.MatchString(header.Get("X-Request-ID")) {
				t.Fatalf("status/headers: got %d %+v %s, want %d cache=%q nosniff=%q", out.Code, header, out.Body, c.Status, c.CacheControl, c.NoSniff)
			}
			if method == http.MethodHead {
				if out.Body.Len() != 0 {
					t.Fatalf("HEAD wrote body: %s", out.Body)
				}
				return
			}
			got := timestamps.ReplaceAllStringFunc(out.Body.String(), func(match string) string {
				key := fieldPattern.FindStringSubmatch(match)[1]
				return fmt.Sprintf(`"%s":"%s"`, key, fixture.Times[key])
			})
			var want bytes.Buffer
			if err := json.Compact(&want, c.Body); err != nil {
				t.Fatal(err)
			}
			if got != want.String() {
				t.Fatalf("got %s\nwant %s", got, want.String())
			}
		})
	}
	for _, c := range fixture.ReviewCases {
		t.Run("review-"+c.Name, func(t *testing.T) {
			if c.GraphEnabled != nil {
				setGraph(t, *c.GraphEnabled)
			}
			review := func() *httptest.ResponseRecorder {
				var input string
				if c.Body != nil {
					input = *c.Body
				}
				req := httptest.NewRequest(http.MethodPost, c.URL, strings.NewReader(input))
				sourceFixtureAuth(req, c.Authorized == nil || *c.Authorized)
				contentType := "application/json"
				if c.ContentType != nil {
					contentType = *c.ContentType
				}
				if contentType != "" {
					req.Header.Set("Content-Type", contentType)
				}
				req.Header.Set("X-Request-ID", "synthetic-spoof")
				out := httptest.NewRecorder()
				app.ServeHTTP(out, req)
				return out
			}
			responses := make([]*httptest.ResponseRecorder, 0, len(c.Result))
			if c.Concurrent {
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- review() }()
				responses = append(responses, review(), <-done)
				slices.SortFunc(responses, func(a, b *httptest.ResponseRecorder) int { return a.Code - b.Code })
			} else {
				responses = append(responses, review())
			}
			if len(responses) != len(c.Result) {
				t.Fatalf("response count: got %d want %d", len(responses), len(c.Result))
			}
			for i, out := range responses {
				want := c.Result[i]
				h := out.Header()
				requestID := h.Get("X-Request-ID")
				if out.Code != want.Status || h.Get("Cache-Control") != want.CacheControl ||
					h.Get("X-Content-Type-Options") != want.NoSniff || !reqIDPattern.MatchString(requestID) {
					t.Fatalf("reply %d: status/headers got %d %+v %s, want %d cache=%q nosniff=%q", i, out.Code, h, out.Body, want.Status, want.CacheControl, want.NoSniff)
				}
				got := timestamps.ReplaceAllStringFunc(out.Body.String(), func(match string) string {
					key := fieldPattern.FindStringSubmatch(match)[1]
					return fmt.Sprintf(`"%s":"%s"`, key, fixture.Times[key])
				})
				got = strings.Replace(got, `"requestId":"`+requestID+`"`, `"requestId":"@request-id"`, 1)
				var body bytes.Buffer
				if err := json.Compact(&body, want.Body); err != nil {
					t.Fatal(err)
				}
				if got != body.String() {
					t.Fatalf("got %s\nwant %s", got, body.String())
				}
			}
		})
	}
	req := httptest.NewRequest(http.MethodPost, "/api/memory-relations", nil)
	sourceFixtureAuth(req, true)
	out := httptest.NewRecorder()
	app.ServeHTTP(out, req)
	if out.Code != http.StatusServiceUnavailable || !bytes.Contains(out.Body.Bytes(), []byte("This API has not yet migrated to the Go preview")) {
		t.Fatalf("out-of-scope collection POST returned %d %s", out.Code, out.Body)
	}
}
