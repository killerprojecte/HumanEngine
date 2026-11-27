package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"strconv"
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

func scenePublicationFixture(t testing.TB, initialize storage.ConnectionInitializer) (*Server, *sql.DB) {
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
	return s, main
}

func TestSceneHistoryDoesNotRereadThePublicationGate(t *testing.T) {
	var reads atomic.Int64
	s, _ := scenePublicationFixture(t, func(c *sqlite3.SQLiteConn) error {
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

func TestSceneHistoryReprojectsAfterAConcurrentRevocation(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		local                         bool
		revocations, materializations int
	}{
		{"local-refreshes", true, 1, 2},
		{"local-fails-on-second-race", true, 2, 2},
		{"remote-does-not-repeat-provider-work", false, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, main := scenePublicationFixture(t, nil)
			ctx := t.Context()
			gate, err := recovery.ReadGate(ctx, s.main)
			if err != nil {
				t.Fatal(err)
			}
			writer, err := recovery.OpenWriter(ctx, gate.Authority)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			reads, writes := 0, 0
			err = publishSceneRead(ctx, s.runtimeStatus.publisher, tc.local, func() (sceneReadSnapshot, error) {
				var viewed recovery.Gate
				err := s.runtimeStatus.publisher.PublishWithGate(ctx, func(_ *sql.Conn, g recovery.Gate) ([]byte, error) {
					viewed = g
					return nil, nil
				}, func([]byte) error { return nil })
				if err != nil {
					return sceneReadSnapshot{}, err
				}
				reads++
				body := []byte("fresh")
				if reads <= tc.revocations {
					body = []byte("revoked")
					// Commit a real revocation exactly after projection and before
					// its output can claim the next publication reservation.
					_, err = writer.Write(ctx, main, []recovery.Revocation{
						recovery.Source{SceneID: "scene", MessageID: tc.name + "-" + strconv.Itoa(reads)},
					}, recovery.ReaderCapability, func(*recovery.MainMutation) error { return nil })
				}
				return sceneReadSnapshot{observed: viewed, status: 200, body: body}, err
			}, func(data []byte, status int) error {
				writes++
				if string(data) != "fresh" || status != 200 {
					t.Fatalf("published stale bytes: %s (%d)", data, status)
				}
				return nil
			})
			if reads != tc.materializations {
				t.Fatalf("materialized %d times, wanted %d", reads, tc.materializations)
			}
			if tc.local && tc.revocations == 1 {
				if err != nil || writes != 1 {
					t.Fatalf("fresh bytes not published exactly once: %d %v", writes, err)
				}
			} else if !errors.Is(err, errSceneReadStale) || writes != 0 {
				t.Fatalf("stale read did not fail closed: %d %v", writes, err)
			}
		})
	}
}

func TestLocalHistoryWaitsOnlyForItsKnownIntent(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		localWriter, commits bool
	}{
		{"committed-local-intent", true, true},
		{"uncommitted-local-intent", true, false},
		{"unrelated-authority-handle", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, main := scenePublicationFixture(t, nil)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			gate, err := recovery.ReadGate(ctx, main)
			if err != nil {
				t.Fatal(err)
			}
			writer, err := recovery.OpenWriter(ctx, gate.Authority)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			origin := s.authority
			if tc.localWriter {
				origin = writer
			}
			publisher, err := recovery.NewReadPublisher(ctx, main, origin)
			if err != nil {
				t.Fatal(err)
			}
			defer publisher.CloseAndWait(context.Background())
			appended, settle := make(chan struct{}), make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				finished <- writer.WithWriteSession(ctx, main, func(tx *recovery.WriteSession) error {
					if _, err := tx.AppendIntent(recovery.Source{SceneID: "scene", MessageID: "private-recall"}); err != nil {
						return err
					}
					close(appended)
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-settle:
					}
					if !tc.commits {
						return errors.New("fixture_main_rollback")
					}
					return nil
				})
			}()
			select {
			case <-appended:
			case err := <-finished:
				t.Fatal("writer did not append", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			firstRejected := make(chan struct{})
			emits := 0
			attempts := 0
			finishedRead := make(chan error, 1)
			go func() {
				finishedRead <- publishSceneRead(ctx, publisher, true, func() (sceneReadSnapshot, error) {
					attempts++
					var snapshot sceneReadSnapshot
					err := publisher.PublishWithGate(ctx, func(_ *sql.Conn, g recovery.Gate) ([]byte, error) {
						snapshot = sceneReadSnapshot{observed: g, status: 200, body: []byte("fresh")}
						return nil, nil
					}, func([]byte) error { return nil })
					if attempts == 1 {
						close(firstRejected)
					}
					return snapshot, err
				}, func(data []byte, status int) error {
					if string(data) != "fresh" || status != 200 {
						return errors.New("bad publication")
					}
					emits++
					return nil
				})
			}()
			select {
			case <-firstRejected:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !tc.localWriter {
				// A separate writer's gap must fail closed immediately rather
				// than waiting for a local completion signal.
				select {
				case err := <-finishedRead:
					if err == nil || emits != 0 || attempts != 1 {
						t.Fatalf("foreign head allowed publication: %v, %d attempts, %d emits", err, attempts, emits)
					}
				case <-ctx.Done():
					t.Fatal("reader waited on a foreign writer")
				}
				close(settle)
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
				return
			}
			close(settle)
			writeErr := <-finished
			readErr := <-finishedRead
			if tc.commits {
				if writeErr != nil || readErr != nil || emits != 1 || attempts != 2 {
					t.Fatalf("local commit did not release one fresh read: %v %v, %d attempts, %d emits", writeErr, readErr, attempts, emits)
				}
			} else if writeErr == nil || readErr == nil || emits != 0 || attempts != 1 {
				t.Fatalf("unacknowledged intent allowed publication: %v %v, %d attempts, %d emits", writeErr, readErr, attempts, emits)
			}
		})
	}
}

func BenchmarkSceneHistoryPublication(b *testing.B) {
	for _, source := range []string{"local", "remote"} {
		b.Run(source, func(b *testing.B) {
			s, _ := scenePublicationFixture(b, nil)
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
