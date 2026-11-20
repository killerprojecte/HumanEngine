package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

func TestEmbeddingIndexAdminActualSource(t *testing.T) {
	var fixture struct {
		Node     string
		Sources  map[string]string
		Scene    json.RawMessage
		Records  []json.RawMessage
		Spaces   [][]json.RawMessage
		Vectors  [][2]string
		Proposal struct{ Context, Candidate json.RawMessage }
		Cases    []struct {
			Name, Method string
			Auth         bool
			Set          []struct {
				Key   string
				Value json.RawMessage
			}
			Status                int
			Headers               map[string]string
			Body, Stored, Learned any
			LearnedVector         int
		}
	}
	data, err := os.ReadFile("testdata/embedding-admin-status-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil || fixture.Node != "v24.22.1" || len(fixture.Cases) != 15 {
		t.Fatalf("invalid TypeScript oracle: %d %v", len(fixture.Cases), err)
	}
	for path, expected := range fixture.Sources {
		data, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != expected {
			t.Fatalf("source drift: %s (%v)", path, err)
		}
	}

	app, db := memoryDeleteApplication(t, "synthetic-token")
	appRequest(t, app, http.MethodPut, "/api/scenes/s", fixture.Scene)
	const suppliedID = "12345678-1234-4123-8123-123456789012"
	requestID := regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
	var learnedID string
	var sourceLease, goLease float64
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Oracle clock advances after Hono/Engine open. Keep both future and past
	// deadlines independent of the Go host clock, without sleeping or changing
	// the production clock. Compare the entire job after normalizing its epoch.
	now := time.Now()
	normalizeLease := func(value any) {
		job, _ := value.(map[string]any)
		if when, ok := job["leaseUntil"].(float64); ok && when == goLease {
			job["leaseUntil"] = sourceLease
		}
	}

	for _, c := range fixture.Cases {
		if ok := t.Run(c.Name, func(t *testing.T) {
			for _, entry := range c.Set {
				if entry.Value == nil {
					exec(t, "DELETE FROM config WHERE key=?", entry.Key)
					continue
				}
				if entry.Key == "embedding.rebuild" {
					var job map[string]any
					if err := json.Unmarshal(entry.Value, &job); err != nil {
						t.Fatal(err)
					}
					if lease, ok := job["leaseUntil"].(float64); ok {
						sourceLease = lease
						goLease = float64(now.Add(-time.Hour).UnixMilli())
						job["leaseUntil"] = goLease
						entry.Value, err = json.Marshal(job)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				exec(t, "INSERT OR REPLACE INTO config(key,value) VALUES(?,?)", entry.Key, string(entry.Value))
			}
			if c.Name == "peer-running" || c.Name == "normal-expiry" {
				before := goLease
				goLease = float64(now.Add(-time.Hour).UnixMilli())
				if c.Name == "peer-running" {
					goLease = float64(now.Add(time.Hour).UnixMilli())
				}
				exec(t, "UPDATE config SET value=json_set(value,'$.leaseUntil',?) WHERE key='embedding.rebuild' AND json_extract(value,'$.leaseUntil')=?", int64(goLease), int64(before))
			}
			if c.Name == "ready" {
				for _, raw := range fixture.Records {
					var record struct {
						ID, Content string
						Scope       struct{ PersonID *string }
					}
					if err := json.Unmarshal(raw, &record); err != nil {
						t.Fatal(err)
					}
					exec(t, "INSERT INTO memories VALUES(?,?,?,?,?,?)", record.ID, "scene:s", record.Scope.PersonID, record.Content, record.ID, string(raw))
				}
				for _, space := range fixture.Spaces {
					var id string
					if err := json.Unmarshal(space[0], &id); err != nil {
						t.Fatal(err)
					}
					exec(t, "INSERT INTO vector_spaces VALUES(?,?)", id, string(space[1]))
				}
				for _, vector := range fixture.Vectors {
					exec(t, "INSERT INTO memory_vectors VALUES(?,?,?)", vector[0], vector[1], make([]byte, 8))
				}
				saved, err := app.Semantic.Memory.Propose(t.Context(), memoryPatchObject(t, fixture.Proposal.Context), memoryPatchObject(t, fixture.Proposal.Candidate), memoryservice.ProposalOptions{})
				if err != nil || saved.Status != "saved" {
					t.Fatalf("fixture proposal: %+v %v", saved, err)
				}
				learnedID = field(saved.Memory, "id").(string)
				exec(t, "INSERT INTO memory_vectors VALUES(?,?,?)", learnedID, "space-active", make([]byte, 8))
			}
			if c.Name == "unauthorized-expired" {
				exec(t, "INSERT OR IGNORE INTO recall_tombstones VALUES(?,?,?)", "s", "fixture-message", "2026-01-03T00:00:00.000Z")
			}
			req := httptest.NewRequest(c.Method, "/api/embedding-index", nil)
			req.Header.Set("X-Request-ID", suppliedID)
			sourceFixtureAuth(req, c.Auth)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			var body any
			if rec.Body.Len() != 0 {
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err, rec.Body.String())
				}
			}
			if v, ok := body.(map[string]any); ok {
				normalizeLease(v["job"])
			}
			if rec.Code != c.Status || !reflect.DeepEqual(body, c.Body) {
				t.Fatalf("HTTP %d %v, want %d %v", rec.Code, body, c.Status, c.Body)
			}
			for header, expected := range c.Headers {
				if actual := rec.Header().Get(header); actual != expected {
					t.Fatalf("%s = %q, want %q", header, actual, expected)
				}
			}
			if id := rec.Header().Get("X-Request-ID"); id == suppliedID || !requestID.MatchString(id) {
				t.Fatalf("invalid request ID %q", id)
			}
			var raw string
			err := db.QueryRow("SELECT value FROM config WHERE key='embedding.rebuild'").Scan(&raw)
			var stored any
			if err == nil {
				if err := json.Unmarshal([]byte(raw), &stored); err != nil {
					t.Fatal(err)
				}
			} else if err != sql.ErrNoRows {
				t.Fatal(err)
			}
			normalizeLease(stored)
			if !reflect.DeepEqual(stored, c.Stored) {
				t.Fatalf("persisted lease state %v, want %v", stored, c.Stored)
			}
			var learned any
			var vectors int
			if learnedID != "" {
				var status string
				if err := db.QueryRow("SELECT json_extract(value,'$.status') FROM memories WHERE id=?", learnedID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow("SELECT count(*) FROM memory_vectors WHERE memory_id=?", learnedID).Scan(&vectors); err != nil {
					t.Fatal(err)
				}
				learned = status
			}
			if learned != c.Learned || vectors != c.LearnedVector {
				t.Fatalf("persisted provenance: %v,%d, want %v,%d", learned, vectors, c.Learned, c.LearnedVector)
			}
		}); !ok {
			t.FailNow()
		}
	}
}
