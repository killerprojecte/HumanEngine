package sourceevents

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestRuntimeRecallRootBindingAndLease(t *testing.T) {
	f, base := runtimeFixture(t, Options{})
	other, _ := runtimeFixture(t, Options{})
	ctx := context.Background()
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group"}}
	events := []string{}
	var held *recovery.WriteSession
	var heldReads botruntime.ReadAuthority
	a := base.WithRecallPreparation(func(_ context.Context, s *recovery.WriteSession, reads botruntime.ReadAuthority, got botruntime.Scene, id string) (func() error, error) {
		held, heldReads = s, reads
		events = append(events, "capture-memory")
		if got.ID != scene.ID || id != "message" {
			t.Fatal(got, id)
		}
		if e := s.CheckBinding(f.authority, f.main); e != nil {
			return nil, e
		}
		if e := s.CheckBinding(other.authority, other.main); e == nil {
			t.Fatal("accepted foreign owner")
		}
		if recalled, e := reads.IsRecalled(scene, id); e != nil || recalled {
			t.Fatal(recalled, e)
		}
		return func() error {
			events = append(events, "memory")
			if recalled, e := reads.IsRecalled(scene, id); e != nil || !recalled {
				t.Fatal(recalled, e)
			}
			return s.WithSavepoint(func(child *recovery.WriteSession) error {
				if e := s.CheckBinding(f.authority, f.main); e == nil {
					t.Fatal("parent active inside child")
				}
				_, e := child.Exec("INSERT INTO config VALUES('memory','done')")
				return e
			})
		}, nil
	})
	if base.recallPreparation != nil {
		t.Fatal("mutated original facade")
	}
	err := a.RecallWithSession(ctx, scene, "message", func(s *recovery.WriteSession, reads botruntime.ReadAuthority) (func() error, error) {
		if held != s || heldReads != reads {
			t.Fatal("different owner")
		}
		events = append(events, "capture-runtime")
		return func() error {
			events = append(events, "runtime")
			rows, e := s.Query("SELECT value FROM config WHERE key='memory'")
			if e != nil {
				return e
			}
			defer rows.Close()
			if !rows.Next() {
				t.Fatal("memory callback skipped")
			}
			return rows.Err()
		}, nil
	}, func() error {
		events = append(events, "apply")
		if e := heldReads.Ready(); !errors.Is(e, sql.ErrTxDone) {
			t.Fatal("read lease active in apply", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"capture-memory", "capture-runtime", "apply", "memory", "runtime"}) {
		t.Fatal(events)
	}
	if _, e := held.Exec("INSERT INTO config VALUES('escaped','bad')"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	if e := heldReads.Ready(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	head, e := f.authority.Head(ctx)
	if e != nil || head.Sequence != 1 {
		t.Fatal("root intent duplicated", head, e)
	}
}

func TestRuntimeRecallBoundFailuresPreserveIntentNotCompletion(t *testing.T) {
	for _, phase := range []string{"capture-memory", "capture-runtime", "apply", "memory", "runtime", "native-loss"} {
		t.Run(phase, func(t *testing.T) {
			f, base := runtimeFixture(t, Options{})
			if phase == "native-loss" {
				if _, e := f.main.Exec("CREATE TRIGGER lose_recall BEFORE INSERT ON config BEGIN SELECT RAISE(ROLLBACK,'synthetic_loss'); END"); e != nil {
					t.Fatal(e)
				}
			}
			wanted := errors.New("synthetic_" + phase)
			var held *recovery.WriteSession
			completed := false
			a := base.WithRecallPreparation(func(_ context.Context, s *recovery.WriteSession, _ botruntime.ReadAuthority, _ botruntime.Scene, _ string) (func() error, error) {
				held = s
				if phase == "capture-memory" {
					return nil, wanted
				}
				return func() error {
					if phase == "memory" {
						return wanted
					}
					_, e := s.Exec("INSERT INTO config VALUES('memory','done')")
					if phase == "native-loss" {
						if e == nil {
							t.Fatal("missing native loss")
						}
						if _, e = s.Exec("INSERT INTO config VALUES('escaped','bad')"); !errors.Is(e, sql.ErrTxDone) {
							t.Fatal(e)
						}
						return nil // Swallowing a statement failure cannot restore a lost transaction.
					}
					return e
				}, nil
			})
			scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group"}}
			err := a.Recall(context.Background(), scene, "message", func(botruntime.ReadAuthority) (func() error, error) {
				if phase == "capture-runtime" {
					return nil, wanted
				}
				return func() error {
					if phase == "runtime" {
						return wanted
					}
					if e := held.AssertReady(); e != nil {
						return e
					}
					completed = true
					return nil
				}, nil
			}, func() error {
				if phase == "apply" {
					return wanted
				}
				return nil
			})
			if err == nil || (phase != "native-loss" && !errors.Is(err, wanted)) {
				t.Fatal(err)
			}
			if completed {
				t.Fatal("business callback completed")
			}
			head, e := f.authority.Head(context.Background())
			if e != nil || head.Sequence != 1 {
				t.Fatal(head, e)
			}
			gate, e := recovery.ReadGate(context.Background(), f.main)
			if e != nil || gate.Head.Sequence != 0 {
				t.Fatal("false completion ACK", gate, e)
			}
			var count int
			if e = f.main.QueryRow("SELECT count(*) FROM recall_tombstones").Scan(&count); e != nil || count != 0 {
				t.Fatal(count, e)
			}
			if e = f.main.QueryRow("SELECT count(*) FROM config").Scan(&count); e != nil || count != 0 {
				t.Fatal(count, e)
			}
			if _, e = held.Exec("INSERT INTO config VALUES('expired','bad')"); !errors.Is(e, sql.ErrTxDone) {
				t.Fatal(e)
			}
			if e = a.Ready(); e == nil {
				t.Fatal("pending root intent admitted reads")
			}
		})
	}
}

func TestRuntimeRecallCompletionFollowsActualRootCommit(t *testing.T) {
	for _, phase := range []string{"success", "root-failure", "completion-failure"} {
		t.Run(phase, func(t *testing.T) {
			f, base := runtimeFixture(t, Options{})
			// Acquiring a new writer in completion must not wait for its own root writer.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			wanted := errors.New("synthetic_" + phase)
			var held *recovery.WriteSession
			prepared, completed := 0, 0
			a := base.WithRecallPreparation(func(_ context.Context, s *recovery.WriteSession, _ botruntime.ReadAuthority, _ botruntime.Scene, _ string) (func() error, error) {
				held = s
				return func() error {
					prepared++
					if phase == "root-failure" {
						return wanted
					}
					_, e := s.Exec("INSERT INTO config VALUES('business','done')")
					return e
				}, nil
			}).WithRecallCompletion(func(got context.Context) error {
				completed++
				if got != ctx {
					t.Fatal("completion lost operation context")
				}
				if _, e := held.Exec("INSERT INTO config VALUES('escaped','bad')"); !errors.Is(e, sql.ErrTxDone) {
					t.Fatal("root still usable", e)
				}
				var count int
				if e := f.main.QueryRowContext(got, "SELECT count(*) FROM config WHERE key='business'").Scan(&count); e != nil || count != 1 {
					t.Fatal("business work not committed", count, e)
				}
				if phase == "completion-failure" {
					return wanted
				}
				return f.authority.WithWriteSession(got, f.main, func(s *recovery.WriteSession) error {
					_, e := s.Exec("INSERT INTO config VALUES('projection','flushed')")
					return e
				})
			})
			if base.recallPreparation != nil || base.recallCompletion != nil {
				t.Fatal("original facade mutated")
			}
			if a.WithRecallCompletion(nil).recallCompletion != nil || a.recallCompletion == nil {
				t.Fatal("completion removal mutated original")
			}
			scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group"}}
			err := a.Recall(ctx, scene, "message", nil, nil)
			if phase == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, wanted) {
				t.Fatal(err)
			}
			wantComplete := 1
			if phase == "root-failure" {
				wantComplete = 0
			}
			if prepared != 1 || completed != wantComplete {
				t.Fatal("callback invocation count", prepared, completed)
			}
			var count int
			if e := f.main.QueryRow("SELECT count(*) FROM recall_tombstones").Scan(&count); e != nil || count != wantComplete {
				t.Fatal("root outcome changed by completion", count, e)
			}
		})
	}
}
