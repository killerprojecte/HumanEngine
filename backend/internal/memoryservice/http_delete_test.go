package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestMemoryDeletePublicationActualSourcePrivacyPrestates(t *testing.T) {
	data := readPrivacyFixture(t)
	count := 0
	for _, c := range data.Cases {
		for i, step := range c.Steps {
			if step.Operation != "delete" {
				continue
			}
			count++
			t.Run(fmt.Sprintf("%s/%d", c.Name, i), func(t *testing.T) {
				s, f := privacyFixed(t, step.Before)
				privacySeed(t, f, data.Schemas, step.Before)
				var id string
				if err := json.Unmarshal(step.Args[0], &id); err != nil {
					t.Fatal(err)
				}
				calls := 0
				err := s.PublishDelete(context.Background(), f.main, f.recovery, id, func(b []byte) error {
					calls++
					if string(b) != `{"ok":`+string(step.Result)+`}` {
						t.Fatalf("response %s want %s", b, step.Result)
					}
					return nil
				})
				if err != nil || calls != 1 {
					t.Fatalf("publication calls=%d error=%v", calls, err)
				}
				got, err := privacyNormalizeFreshJobGenerations(privacySnapshot(t, f.main), step.After, step.Before)
				if err != nil {
					t.Fatal(err)
				}
				privacyCompare(t, got, step.After)
			})
		}
	}
	if count != 8 {
		t.Fatalf("delete fixture coverage %d", count)
	}
}

func TestMemoryDeletePublicationOwnership(t *testing.T) {
	s, f := serviceSetup(t)
	_, foreign := serviceSetup(t)
	if !s.OwnedBy(f.main, f.recovery) || s.OwnedBy(f.main, foreign.recovery) || s.OwnedBy(foreign.main, f.recovery) {
		t.Fatal("root binding")
	}
	called := false
	emit := func([]byte) error { called = true; return nil }
	if err := s.PublishDelete(context.Background(), foreign.main, f.recovery, "missing", emit); err == nil || called {
		t.Fatal("foreign pool accepted", err)
	}
	if err := s.PublishDelete(context.Background(), f.main, foreign.recovery, "missing", emit); err == nil || called {
		t.Fatal("foreign authority accepted", err)
	}
	if err := f.recovery.WithWriteSession(context.Background(), f.main, func(ws *recovery.WriteSession) error {
		d := s.d
		d.Assertions = d.Assertions.WithSession(ws)
		borrowed, err := New(d)
		if err != nil {
			return err
		}
		if borrowed.OwnedBy(f.main, f.recovery) {
			return errors.New("borrowed owner accepted")
		}
		if err = borrowed.PublishDelete(context.Background(), f.main, f.recovery, "missing", emit); err == nil {
			return errors.New("borrowed publication accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryDeletePublicationFailureRetainsIntentWithoutACK(t *testing.T) {
	data, step := privacyCase(t, "semantic-partition-delete", "delete")
	s, f := privacyFixed(t, step.Before)
	privacySeed(t, f, data.Schemas, step.Before)
	if _, err := f.main.Exec("UPDATE memory_semantic_candidates SET value='{' "); err != nil {
		t.Fatal(err)
	}
	before := privacySnapshot(t, f.main)
	head, err := f.recovery.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err = json.Unmarshal(step.Args[0], &id); err != nil {
		t.Fatal(err)
	}
	called := false
	err = s.PublishDelete(context.Background(), f.main, f.recovery, id, func([]byte) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("failed capture published success", err)
	}
	after, err := f.recovery.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gate, err := recovery.ReadGate(context.Background(), f.main)
	if err != nil {
		t.Fatal(err)
	}
	if after.Sequence != head.Sequence+1 || gate.Head.Sequence != head.Sequence {
		t.Fatal("intent/ACK", head, after, gate)
	}
	privacyCompare(t, privacySnapshot(t, f.main), before)
}

// A disabled source projection owner still executes its real AfterCommit gate.
// Hold that gate to create a deterministic committed-before-acceptance window,
// without adding a production test hook or replacing the deletion lifecycle.
func deletePublicationBarrier(t *testing.T, s *Service, f scopedFixture) (*Service, <-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	p, err := projections.New(context.Background(), f.main, f.recovery, func() bool { close(entered); <-release; return false }, projections.AssertionReader(s.d.Assertions, s.d.Authority), projections.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d := s.d
	d.Projections = p
	s, err = New(d)
	if err != nil {
		t.Fatal(err)
	}
	return s, entered, func() { close(release) }
}

func TestMemoryDeletePublicationCommittedFenceAndCancellation(t *testing.T) {
	for _, mode := range []string{"changed-head", "external-revocation", "missing-id-witness", "cancel", "emit-failure"} {
		t.Run(mode, func(t *testing.T) {
			s, f := serviceSetup(t)
			id := "delete-once"
			if mode != "missing-id-witness" {
				putRecord(t, f, id, "")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			calls := 0
			emitErr := errors.New("synthetic_output_failure")
			emit := func([]byte) error {
				calls++
				if mode == "emit-failure" {
					return emitErr
				}
				return nil
			}
			if mode == "emit-failure" {
				err := s.PublishDelete(ctx, f.main, f.recovery, id, emit)
				if !errors.Is(err, emitErr) || calls != 1 {
					t.Fatal("output failure", calls, err)
				}
			} else {
				var entered <-chan struct{}
				var release func()
				s, entered, release = deletePublicationBarrier(t, s, f)
				done := make(chan error, 1)
				go func() { done <- s.PublishDelete(ctx, f.main, f.recovery, id, emit) }()
				select {
				case <-entered:
				case err := <-done:
					t.Fatal("ended before post-commit gate", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				var err error
				switch mode {
				case "changed-head":
					_, err = f.recovery.Write(ctx, f.main, []recovery.Revocation{recovery.Source{SceneID: "s", MessageID: "other"}}, recovery.ReaderCapability, func(*recovery.MainMutation) error { return nil })
				case "external-revocation":
					var head recovery.Head
					head, err = f.recovery.Head(ctx)
					if err == nil {
						_, err = f.recovery.Append(ctx, recovery.Source{SceneID: "s", MessageID: "other"}, head, false)
					}
				case "missing-id-witness":
					// A same-head ordinary writer changes a read the false result relied on.
					putRecord(t, f, id, "")
				case "cancel":
					cancel()
				}
				release()
				if err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("publication failed to finish")
				}
				if err == nil || calls != 0 {
					t.Fatal("stale/cancelled success", calls, err)
				}
				if mode == "missing-id-witness" && !errors.Is(err, recovery.ErrReadWitnessChanged) {
					t.Fatal("missing-row witness", err)
				}
			}
			var rows int
			if err := f.main.QueryRow("SELECT count(*) FROM memories WHERE id=?", id).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "missing-id-witness" {
				want = 1
			}
			if rows != want {
				t.Fatalf("mutation replay/rollback rows=%d want=%d", rows, want)
			}
		})
	}
}
