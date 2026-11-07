package assertions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type supportPrepareCounts struct {
	Record   int `json:"record"`
	Supports int `json:"supports"`
}

type reentrantPreparedRead struct {
	preparedRead
	before func()
}

func (p *reentrantPreparedRead) Query(args ...any) (Rows, error) {
	p.before()
	return p.preparedRead.Query(args...)
}

type closingPreparedRows struct {
	Rows
	statement preparedRead
}

func (r *closingPreparedRows) Close() error { return errors.Join(r.Rows.Close(), r.statement.Close()) }

// This fixture counts actual PrepareContext calls on one real SQLite connection,
// including failed prepares. It does not use latency or a simulated row cache.
// Explicit ordinary preparation matches the source db.prepare(...).get(...).
func supportReadFixture(t *testing.T) (*Reader, *sql.Conn, *supportPrepareCounts, *int, *bool) {
	t.Helper()
	db, e := sql.Open("sqlite3", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	conn, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { conn.Close(); db.Close() })
	if _, e = conn.ExecContext(context.Background(), `CREATE TABLE memories(id TEXT PRIMARY KEY,value TEXT);CREATE TABLE memory_supports(memory_id TEXT PRIMARY KEY,value TEXT);INSERT INTO memories VALUES('a','{"id":"a","content":"first","nested":{"value":"fresh"}}');INSERT INTO memory_supports VALUES('a','[{"parents":["first"],"sources":[]}]')`); e != nil {
		t.Fatal(e)
	}
	counts, checks, blocked := new(supportPrepareCounts), new(int), new(bool)
	r := &Reader{check: func() error { return nil }, ready: func() error {
		*checks++
		if *blocked {
			return errors.New("synthetic-recovery-block")
		}
		return nil
	}}
	r.prepare = func(q string) (preparedRead, error) {
		switch q {
		case supportRecordSQL:
			counts.Record++
		case supportProofSQL:
			counts.Supports++
		}
		statement, e := conn.PrepareContext(context.Background(), q)
		if e != nil {
			return nil, e
		}
		return &publicationPreparedRead{statement: statement, ctx: context.Background(), check: r.check}, nil
	}
	r.query = func(q string, args ...any) (Rows, error) {
		statement, e := r.prepare(q)
		if e != nil {
			return nil, e
		}
		rows, e := statement.Query(args...)
		if e != nil {
			return nil, errors.Join(e, statement.Close())
		}
		return &closingPreparedRows{rows, statement}, nil
	}
	return r, conn, counts, checks, blocked
}

func TestActualSupportReadStatementTraces(t *testing.T) {
	var oracle struct {
		Traces []struct {
			Name       string
			Operations []any
			Steps      []struct {
				Op, Result, Error string
				Counts            supportPrepareCounts
				Checks            int
			}
		}
	}
	raw, e := os.ReadFile("testdata/support-reads.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	for _, trace := range oracle.Traces {
		t.Run(trace.Name, func(t *testing.T) {
			r, conn, counts, checks, blocked := supportReadFixture(t)
			index := 0
			reenter := ""
			var reentered any
			var currentReader *Reader
			prepare := r.prepare
			r.prepare = func(q string) (preparedRead, error) {
				statement, e := prepare(q)
				if e != nil || q != supportRecordSQL {
					return statement, e
				}
				return &reentrantPreparedRead{preparedRead: statement, before: func() {
					mode := reenter
					reenter = ""
					if mode == "read" {
						var err error
						reentered, _, err = currentReader.Get("a")
						if err != nil {
							t.Fatal(err)
						}
					}
					if mode == "failure" {
						if _, err := conn.ExecContext(context.Background(), "DROP TABLE memory_supports"); err != nil {
							t.Fatal(err)
						}
						if _, err := currentReader.Supports("a"); err == nil || !strings.Contains(err.Error(), "no such table") {
							t.Fatal(err)
						}
					}
				}}, nil
			}
			var run func(*Reader, []any)
			run = func(r *Reader, ops []any) {
				for _, rawOp := range ops {
					op := rawOp.([]any)
					var result any
					err := func() (err error) {
						switch op[0] {
						case "scope":
							return r.WithSupportReadStatements(func(child *Reader) error { run(child, op[1].([]any)); return nil })
						case "rollback":
							if _, err = conn.ExecContext(context.Background(), "SAVEPOINT synthetic_scope"); err != nil {
								return err
							}
							err = r.WithSupportReadStatements(func(child *Reader) error { run(child, op[1].([]any)); return errors.New("synthetic-rollback") })
							_, cleanup := conn.ExecContext(context.Background(), "ROLLBACK TO synthetic_scope; RELEASE synthetic_scope")
							return errors.Join(err, cleanup)
						case "panic":
							return r.WithSupportReadStatements(func(child *Reader) error { run(child, op[1].([]any)); return errors.New("synthetic-exit") })
						case "get":
							currentReader = r
							var v any
							var found bool
							v, found, err = r.Get(op[1].(string))
							if err == nil {
								result = obj("found", found)
								if found {
									_ = result.(*Object).Set("value", v)
								}
							}
						case "supports":
							result, err = r.Supports(op[1].(string))
						case "mutateRecord":
							var v any
							v, _, err = r.Get("a")
							if err == nil {
								_ = get(v, "nested").(*Object).Set("value", "caller mutation")
							}
						case "mutateProof":
							var v any
							v, err = r.Supports("a")
							if err == nil {
								o := v.([]any)[0].(*Object)
								_ = o.Set("parents", append(array(get(o, "parents")), "caller mutation"))
							}
						case "record":
							_, err = conn.ExecContext(context.Background(), "UPDATE memories SET value=? WHERE id=?", op[1], "a")
						case "proof":
							_, err = conn.ExecContext(context.Background(), "UPDATE memory_supports SET value=? WHERE memory_id=?", op[1], "a")
						case "sql":
							_, err = conn.ExecContext(context.Background(), op[1].(string))
						case "block":
							*blocked = op[1].(bool)
						case "reenter":
							reenter = op[1].(string)
						case "reentered":
							result = reentered
						default:
							return fmt.Errorf("unknown operation %v", op[0])
						}
						return err
					}()
					if index >= len(trace.Steps) {
						t.Fatal("unexpected extra step")
					}
					want := trace.Steps[index]
					index++
					category := ""
					if err != nil {
						category = err.Error()
						if strings.Contains(category, "no such table") {
							category = "missing-table"
						}
						if (op[0] == "get" || op[0] == "supports") && category != "missing-table" && category != "synthetic-recovery-block" {
							// Both parsers reject the malformed JSON root; wording is runtime-specific.
							if strings.Contains(category, "json") || strings.Contains(category, "JSON") {
								category = "invalid-json"
							}
						}
					}
					got, e := jsjson.Stringify(result)
					if e != nil {
						t.Fatal(e)
					}
					opRaw, _ := json.Marshal(op)
					if string(opRaw) != want.Op || category != want.Error || got != want.Result || *counts != want.Counts || *checks != want.Checks {
						t.Fatalf("step %d %s: result=%s error=%q prepares=%+v checks=%d; want result=%s error=%q prepares=%+v checks=%d", index-1, op[0], got, category, *counts, *checks, want.Result, want.Error, want.Counts, want.Checks)
					}
				}
			}
			run(r, trace.Operations)
			if index != len(trace.Steps) {
				t.Fatal("missing steps", index, len(trace.Steps))
			}
		})
	}
}

func seedSupportReads(t *testing.T, f fixture) {
	t.Helper()
	if _, e := f.main.Exec(`INSERT INTO memories VALUES('a','scene:s',NULL,'first','fingerprint','{"content":"first"}');INSERT INTO memory_supports VALUES('a','[{"parents":["first"]}]')`); e != nil {
		t.Fatal(e)
	}
}

func countSupportPrepares(r *Reader, counts *supportPrepareCounts) {
	prepare := r.prepare
	r.prepare = func(q string) (preparedRead, error) {
		switch q {
		case supportRecordSQL:
			counts.Record++
		case supportProofSQL:
			counts.Supports++
		}
		return prepare(q)
	}
}

func supportContent(t *testing.T, r *Reader, want string) {
	t.Helper()
	value, found, e := r.Get("a")
	if e != nil || !found || get(value, "content") != want {
		t.Fatalf("content=%v found=%v error=%v want=%s", value, found, e, want)
	}
}

func TestSupportReadsBoundSessionRollbackAndExpiration(t *testing.T) {
	f := newFixture(t)
	seedSupportReads(t, f)
	f.main.SetMaxOpenConns(1)
	counts := new(supportPrepareCounts)
	var escaped *Reader
	rollback := errors.New("synthetic-rollback")
	err := f.store.Transaction(context.Background(), func(s *Store) error {
		return s.Read(context.Background(), func(base *Reader) error {
			countSupportPrepares(base, counts)
			return base.WithSupportReadStatements(func(outer *Reader) error {
				escaped = outer
				supportContent(t, outer, "first")
				if _, e := outer.Supports("a"); e != nil {
					return e
				}
				err := s.Transaction(context.Background(), func(child *Store) error {
					if _, _, e := outer.Get("a"); e == nil || !strings.Contains(e.Error(), "scope_inactive") {
						t.Fatal("warm parent not suspended", e)
					}
					if _, e := outer.Supports("a"); e == nil || !strings.Contains(e.Error(), "scope_inactive") {
						t.Fatal("warm parent supports not suspended", e)
					}
					if e := child.exec("UPDATE memories SET value=? WHERE id='a'", `{"content":"nested"}`); e != nil {
						return e
					}
					return child.Read(context.Background(), func(r *Reader) error {
						countSupportPrepares(r, counts)
						return r.WithSupportReadStatements(func(nested *Reader) error { supportContent(t, nested, "nested"); return rollback })
					})
				})
				if !errors.Is(err, rollback) {
					t.Fatal(err)
				}
				supportContent(t, outer, "first")
				if _, e := outer.Supports("a"); e != nil {
					return e
				}
				if *counts != (supportPrepareCounts{2, 1}) {
					t.Fatal("outer handles not restored", counts)
				}
				if e := s.exec("UPDATE memories SET value=? WHERE id='a'", `{"content":"same transaction"}`); e != nil {
					return e
				}
				supportContent(t, outer, "same transaction")
				return nil
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, e := escaped.Get("a"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("escaped Get", e)
	}
	if _, e := escaped.Supports("a"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("escaped Supports", e)
	}
	if e := escaped.WithSupportReadStatements(func(*Reader) error { t.Fatal("expired callback"); return nil }); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
}

func TestSupportReadsPublicationScopeAndPanicCleanup(t *testing.T) {
	f := newFixture(t)
	seedSupportReads(t, f)
	f.main.SetMaxOpenConns(1)
	counts := new(supportPrepareCounts)
	var escaped *Reader
	err := f.store.Read(context.Background(), func(base *Reader) error {
		countSupportPrepares(base, counts)
		return base.WithSupportReadStatements(func(outer *Reader) error {
			supportContent(t, outer, "first")
			var nested *Reader
			func() {
				defer func() {
					if recover() != "synthetic" {
						t.Fatal("missing panic")
					}
				}()
				_ = outer.WithSupportReadStatements(func(r *Reader) error { nested = r; supportContent(t, r, "first"); panic("synthetic") })
			}()
			if _, _, e := nested.Get("a"); !errors.Is(e, sql.ErrTxDone) {
				t.Fatal(e)
			}
			supportContent(t, outer, "first")
			if *counts != (supportPrepareCounts{Record: 2}) {
				t.Fatal(counts)
			}
			escaped = outer
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, e := escaped.Supports("a"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	// The completed refresh did not close the borrowed main connection.
	if e := f.store.Read(context.Background(), func(r *Reader) error { supportContent(t, r, "first"); return nil }); e != nil {
		t.Fatal(e)
	}
}

func TestSupportReadsWarmAuthorityAndNativeLoss(t *testing.T) {
	for _, nativeLoss := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeLoss), func(t *testing.T) {
			f := newFixture(t)
			seedSupportReads(t, f)
			gate, e := recovery.ReadGate(context.Background(), f.main)
			if e != nil {
				t.Fatal(e)
			}
			other, e := recovery.OpenWriter(context.Background(), gate.Authority)
			if e != nil {
				t.Fatal(e)
			}
			defer other.Close()
			if nativeLoss {
				if _, e = f.main.Exec(`CREATE TRIGGER fault BEFORE UPDATE ON memories BEGIN SELECT RAISE(ROLLBACK,'synthetic'); END`); e != nil {
					t.Fatal(e)
				}
			}
			err := f.store.Transaction(context.Background(), func(s *Store) error {
				return s.reader().WithSupportReadStatements(func(r *Reader) error {
					supportContent(t, r, "first")
					if _, e := r.Supports("a"); e != nil {
						return e
					}
					if nativeLoss {
						if e := s.exec("UPDATE memories SET content='lost'"); !errors.Is(e, sql.ErrTxDone) {
							t.Fatal(e)
						}
						if _, _, e := r.Get("a"); !errors.Is(e, sql.ErrTxDone) {
							t.Fatal(e)
						}
						if _, e := r.Supports("a"); !errors.Is(e, sql.ErrTxDone) {
							t.Fatal(e)
						}
					} else {
						if _, e := other.Append(context.Background(), recovery.Source{SceneID: "s", MessageID: "revoked"}, gate.Head, false); e != nil {
							return e
						}
						if _, _, e := r.Get("a"); e == nil || !strings.Contains(e.Error(), "head_changed") {
							t.Fatal("warm handle hid authority advance", e)
						}
						// Source Supports is raw storage, not an authority gate.
						if _, e := r.Supports("a"); e != nil {
							return e
						}
					}
					return nil
				})
			})
			if nativeLoss {
				if !errors.Is(err, sql.ErrTxDone) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSupportReadsClosedConnectionAndLocalReadiness(t *testing.T) {
	r, conn, counts, checks, blocked := supportReadFixture(t)
	var escaped *Reader
	err := r.WithSupportReadStatements(func(scoped *Reader) error {
		escaped = scoped
		supportContent(t, scoped, "first")
		if _, e := scoped.Supports("a"); e != nil {
			return e
		}
		*blocked = true
		if _, _, e := scoped.Get("a"); e == nil || e.Error() != "synthetic-recovery-block" {
			t.Fatal(e)
		}
		if *counts != (supportPrepareCounts{1, 1}) || *checks != 2 {
			t.Fatal("readiness failure prepared or reused a result", counts, *checks)
		}
		*blocked = false
		supportContent(t, scoped, "first")
		if e := conn.Close(); e != nil {
			return e
		}
		if _, _, e := scoped.Get("a"); !errors.Is(e, sql.ErrConnDone) {
			t.Fatal("closed connection reused a cached row", e)
		}
		if len(scoped.supportReads.handles) != 0 {
			t.Fatal("SQL failure retained handles")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, e := escaped.Supports("a"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	reopened, _, reopenedCounts, _, _ := supportReadFixture(t)
	if e := reopened.WithSupportReadStatements(func(fresh *Reader) error { supportContent(t, fresh, "first"); return nil }); e != nil {
		t.Fatal(e)
	}
	if *reopenedCounts != (supportPrepareCounts{Record: 1}) {
		t.Fatal(reopenedCounts)
	}
}
