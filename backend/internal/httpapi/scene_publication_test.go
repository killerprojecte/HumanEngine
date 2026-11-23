package httpapi

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"github.com/mattn/go-sqlite3"
)

func scenePublicationFixture(t testing.TB, initialize storage.ConnectionInitializer) *Server {
	t.Helper()
	s, main, _, _ := fixtureServer(t)
	logs := observability.New(nil, nil)
	s.logs = logs
	t.Cleanup(func() { _ = logs.Close(context.Background()) })
	if _, err := main.Exec(`INSERT INTO scenes VALUES('scene',
		'{"id":"scene","kind":"group","adapterId":"fixture","accountId":"bot","platformSceneId":"g","name":"Fixture","avatarUrl":null}')`); err != nil {
		t.Fatal(err)
	}
	publisher, err := recovery.NewReadPublisherWithInitializer(t.Context(), s.main, s.authority, initialize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := publisher.CloseAndWait(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := s.WithRuntimeStatus(new(botruntime.Runtime), scheduler.NewModelScheduler(), publisher, "fixture", time.Now()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSceneHistoryDoesNotRereadThePublicationGate(t *testing.T) {
	var reads atomic.Int64
	s := scenePublicationFixture(t, func(c *sqlite3.SQLiteConn) error {
		c.RegisterAuthorizer(func(op int, table, column, db string) int {
			if op == sqlite3.SQLITE_READ && table == "recovery_gate" && column == "value" && db == "main" {
				reads.Add(1)
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	})
	for _, tc := range []struct {
		name, body string
		reads      int64
	}{
		{"local", `{"items":[],"coverage":"partial","reason":"local_history_window"}`, 4},
		{"remote", `{"items":[],"coverage":"partial","reason":"adapter_unavailable"}`, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads.Store(0)
			req := httptest.NewRequest("GET", "/api/scenes/scene/history?source="+tc.name, nil)
			req.Header.Set("Authorization", "Bearer "+fixtureToken)
			out := httptest.NewRecorder()
			s.ServeHTTP(out, req)
			if out.Code != 200 || out.Body.String() != tc.body {
				t.Fatalf("history: %d %s", out.Code, out.Body)
			}
			if got := reads.Load(); got != tc.reads {
				t.Fatalf("SQLite gate reads: got %d, want %d", got, tc.reads)
			}
		})
	}
}

func BenchmarkSceneHistoryPublication(b *testing.B) {
	for _, source := range []string{"local", "remote"} {
		b.Run(source, func(b *testing.B) {
			s := scenePublicationFixture(b, nil)
			req := httptest.NewRequest("GET", "/api/scenes/scene/history?source="+source, nil)
			req.Header.Set("Authorization", "Bearer "+fixtureToken)
			b.ReportAllocs()
			for b.Loop() {
				out := httptest.NewRecorder()
				s.ServeHTTP(out, req)
				if out.Code != 200 {
					b.Fatal(out.Code)
				}
			}
		})
	}
}
