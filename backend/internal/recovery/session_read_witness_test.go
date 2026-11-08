package recovery

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func captureFixtureWitness(t *testing.T, f writerFixture, callback func(*WriteSession) error) *ReadWitness {
	t.Helper()
	var w *ReadWitness
	e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		var e error
		w, e = s.CaptureReadWitness(func() error { return callback(s) })
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	return w
}
func verifyFixtureWitness(f writerFixture, w *ReadWitness) error {
	return f.authority.Publish(context.Background(), f.main, func(c *sql.Conn) ([]byte, error) { return nil, w.Verify(context.Background(), c) }, func([]byte) error { return nil })
}
func TestReadWitnessExactQueriesAndPostMutation(t *testing.T) {
	f := newWriterFixture(t)
	if _, e := f.main.Exec("CREATE TABLE wanted(id TEXT PRIMARY KEY,value);INSERT INTO wanted VALUES('a','old');INSERT INTO wanted VALUES('b','unrelated')"); e != nil {
		t.Fatal(e)
	}
	var retained *WriteSession
	w := captureFixtureWitness(t, f, func(s *WriteSession) error {
		retained = s
		rows, e := s.Query("SELECT value FROM wanted WHERE id=?", "a")
		if e != nil {
			return e
		}
		if e = rows.Close(); e != nil {
			return e
		}
		_, e = s.Exec("UPDATE wanted SET value='new' WHERE id='a'")
		return e
	})
	if e := verifyFixtureWitness(f, w); e != nil {
		t.Fatalf("post-write snapshot: %v", e)
	}
	if _, e := f.main.Exec("UPDATE wanted SET value='changed' WHERE id='b'"); e != nil {
		t.Fatal(e)
	}
	if e := verifyFixtureWitness(f, w); e != nil {
		t.Fatalf("unrelated row invalidated witness: %v", e)
	}
	if _, e := f.main.Exec("UPDATE wanted SET value='changed' WHERE id='a'"); e != nil {
		t.Fatal(e)
	}
	if e := verifyFixtureWitness(f, w); !errors.Is(e, ErrReadWitnessChanged) {
		t.Fatal(e)
	}
	if _, e := retained.CaptureReadWitness(func() error { return nil }); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("expired scope", e)
	}
}
func TestReadWitnessPreparedNestedArgsOrderAndTypes(t *testing.T) {
	f := newWriterFixture(t)
	if _, e := f.main.Exec("CREATE TABLE wanted(id BLOB PRIMARY KEY,value);INSERT INTO wanted VALUES(x'61',1);INSERT INTO wanted VALUES(x'62',2)"); e != nil {
		t.Fatal(e)
	}
	arg := []byte("a")
	w := captureFixtureWitness(t, f, func(s *WriteSession) error {
		return s.WithSavepoint(func(child *WriteSession) error {
			stmt, e := child.PrepareQuery("SELECT id,value FROM wanted WHERE id>=? ORDER BY value LIMIT 2")
			if e != nil {
				return e
			}
			defer stmt.Close()
			rows, e := stmt.Query(arg)
			if e != nil {
				return e
			}
			defer rows.Close()
			if !rows.Next() {
				t.Fatal("missing row")
			}
			var id []byte
			var value int
			if e = rows.Scan(&id, &value); e != nil {
				return e
			}
			arg[0] = 'z'
			return nil
		})
	})
	if e := verifyFixtureWitness(f, w); e != nil {
		t.Fatal("borrowed argument changed witness", e)
	}
	if _, e := f.main.Exec("UPDATE wanted SET value=CAST(value AS TEXT) WHERE id=x'61'"); e != nil {
		t.Fatal(e)
	}
	if e := verifyFixtureWitness(f, w); !errors.Is(e, ErrReadWitnessChanged) {
		t.Fatal("SQL type ignored", e)
	}
}
func TestReadWitnessFailuresAndRecursion(t *testing.T) {
	f := newWriterFixture(t)
	err := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		sentinel := errors.New("callback failed")
		if w, e := s.CaptureReadWitness(func() error { return sentinel }); w != nil || !errors.Is(e, sentinel) {
			t.Fatal(w, e)
		}
		_, e := s.CaptureReadWitness(func() error {
			if _, e := s.CaptureReadWitness(func() error { return nil }); e == nil {
				t.Fatal("recursive capture accepted")
			}
			if _, e := s.Query("SELECT 1; DELETE FROM data"); e == nil {
				t.Fatal("multi-statement replay accepted")
			}
			rows, e := s.Query("SELECT value FROM data")
			if e != nil {
				return e
			}
			return rows.Close()
		})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestReadWitnessRollbackAndIncompleteRows(t *testing.T) {
	f := newWriterFixture(t)
	if _, e := f.main.Exec("CREATE TRIGGER fail BEFORE UPDATE ON data BEGIN SELECT RAISE(ROLLBACK,'synthetic'); END"); e != nil {
		t.Fatal(e)
	}
	err := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		_, e := s.CaptureReadWitness(func() error {
			rows, e := s.Query("SELECT value FROM data")
			if e != nil {
				return e
			}
			defer rows.Close()
			if _, e = s.Exec("UPDATE data SET value='x'"); e == nil {
				t.Fatal("rollback absent")
			}
			return nil
		})
		return e
	})
	if err == nil {
		t.Fatal("lost transaction accepted")
	}
}

func TestReadWitnessOrderedPageMembership(t *testing.T) {
	f := newWriterFixture(t)
	if _, e := f.main.Exec("CREATE TABLE page(id TEXT PRIMARY KEY,value);INSERT INTO page VALUES('b','B'),('c','C')"); e != nil {
		t.Fatal(e)
	}
	w := captureFixtureWitness(t, f, func(s *WriteSession) error {
		rows, e := s.Query("SELECT id,value FROM page ORDER BY id LIMIT ?", 1)
		if e != nil {
			return e
		}
		defer rows.Close()
		return rows.Err()
	})
	if _, e := f.main.Exec("INSERT INTO page VALUES('z','later')"); e != nil {
		t.Fatal(e)
	}
	if e := verifyFixtureWitness(f, w); e != nil {
		t.Fatal("unrelated page tail", e)
	}
	if _, e := f.main.Exec("INSERT INTO page VALUES('a','earlier')"); e != nil {
		t.Fatal(e)
	}
	if e := verifyFixtureWitness(f, w); !errors.Is(e, ErrReadWitnessChanged) {
		t.Fatal("membership changed without invalidation", e)
	}
}
func TestReadWitnessPanicAndQueryErrorsReleaseCapture(t *testing.T) {
	f := newWriterFixture(t)
	e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("panic missing")
				}
			}()
			_, _ = s.CaptureReadWitness(func() error { panic("synthetic") })
		}()
		if _, e := s.CaptureReadWitness(func() error { _, e := s.Query("SELECT missing_column FROM data"); return e }); e == nil {
			t.Fatal("query error ignored")
		}
		_, e := s.CaptureReadWitness(func() error {
			rows, e := s.Query("SELECT value FROM data")
			if e != nil {
				return e
			}
			return rows.Close()
		})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}
