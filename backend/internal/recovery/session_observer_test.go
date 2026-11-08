package recovery

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestAfterRootWaitsForPhysicalCommitOrRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			f := newWriterFixture(t)
			f.main.SetMaxOpenConns(1)
			called := 0
			var retained *WriteSession
			e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
				retained = s
				if e := s.AfterRoot(func(outcome RootOutcome) {
					expected := RootCommitted
					if fail {
						expected = RootRolledBack
					}
					if outcome.Status != expected {
						t.Fatal(outcome)
					}
					called++
					var count int
					if e := f.main.QueryRow("SELECT COUNT(*) FROM data").Scan(&count); e != nil {
						t.Fatal(e)
					}
					if e := f.authority.WithWriteSession(context.Background(), f.main, func(*WriteSession) error { return nil }); e != nil {
						t.Fatal("observer still held root owner", e)
					}
				}); e != nil {
					return e
				}
				if called != 0 {
					t.Fatal("inline observer")
				}
				if fail {
					return errors.New("synthetic rollback")
				}
				return nil
			})
			if fail != (e != nil) || called != 1 {
				t.Fatal(e, called)
			}
			if retained.AfterRoot(func(RootOutcome) { t.Fatal("expired observer") }) == nil {
				t.Fatal("expired writer accepted observer")
			}
		})
	}
}
func TestAfterRootUncertainAndPanicIsolation(t *testing.T) {
	f := newWriterFixture(t)
	called := 0
	err := f.authority.withWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		if e := s.AfterRoot(func(outcome RootOutcome) {
			if outcome.Status != RootUncertain {
				t.Fatal(outcome)
			}
			called++
			panic("isolated observer")
		}); e != nil {
			return e
		}
		return s.AfterRoot(func(outcome RootOutcome) {
			if outcome.Status != RootUncertain {
				t.Fatal(outcome)
			}
			called++
		})
	}, func(ctx context.Context, conn *sql.Conn, _ string) error {
		if _, e := conn.ExecContext(ctx, "COMMIT"); e != nil {
			return e
		}
		return errors.New("synthetic-after-commit")
	})
	if !errors.Is(err, ErrCommitOutcomeUnknown) || called != 2 {
		t.Fatal(err, called)
	}
}
func TestAfterRootBodyPanicPreservesOutcomeAndObserverOrder(t *testing.T) {
	f := newWriterFixture(t)
	trace := []int{}
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		_ = f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
			for i := 0; i < 3; i++ {
				i := i
				if e := s.AfterRoot(func(outcome RootOutcome) {
					if outcome.Status != RootRolledBack {
						t.Fatal(outcome)
					}
					trace = append(trace, i)
					if e := s.AfterRoot(func(RootOutcome) {}); e == nil {
						t.Fatal("sealed callback registration accepted")
					}
				}); e != nil {
					return e
				}
			}
			panic("synthetic writer panic")
		})
	}()
	if !panicked || len(trace) != 3 || trace[0] != 0 || trace[1] != 1 || trace[2] != 2 {
		t.Fatal(panicked, trace)
	}
}
