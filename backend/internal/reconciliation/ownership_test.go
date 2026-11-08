package reconciliation

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestConcreteOwnerChildAndExpiry(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	input := parseObject(t, `{"ctx":{"sceneId":"s","provenance":"contextual"},"scope":{"kind":"scene","sceneId":"s"},"candidate":{"content":"Synthetic"},"attribution":{},"parents":[]}`)
	if _, e := New(nil, testOptions(t)); e == nil {
		t.Fatal("nil owner accepted")
	}
	var escaped *Policy
	var escapedReader *assertions.Reader
	e := f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
		return f.store.WithSession(session).Read(ctx, func(reader *assertions.Reader) error {
			if _, e := New(reader, assertionauthority.Options{}); e == nil {
				t.Fatal("missing parser accepted")
			}
			p, e := New(reader, testOptions(t))
			if e != nil {
				return e
			}
			escaped = p
			escapedReader = reader
			check := func(want string) {
				t.Helper()
				r, e := inspectInput(p, input)
				if e != nil {
					t.Fatal(e)
				}
				got := r.Status
				if got == "rejected" {
					got = r.Reason
				}
				if got != want {
					t.Fatalf("got %s want %s", got, want)
				}
			}
			check("accepted")
			rollback := errors.New("synthetic rollback")
			e = session.WithSavepoint(func(child *recovery.WriteSession) error {
				if _, e := inspectInput(p, input); e == nil {
					t.Fatal("ancestor policy usable with active child")
				}
				return f.store.WithSession(child).Read(ctx, func(cr *assertions.Reader) error {
					cp, e := New(cr, testOptions(t))
					if e != nil {
						return e
					}
					r, e := inspectInput(cp, input)
					if e != nil {
						return e
					}
					if _, e = child.Exec("INSERT INTO memory_deleted VALUES(?,'now')", r.FP); e != nil {
						return e
					}
					r, e = inspectInput(cp, input)
					if e != nil {
						return e
					}
					if r.Reason != "manually_deleted" {
						t.Fatal(r)
					}
					return rollback
				})
			})
			if !errors.Is(e, rollback) {
				return e
			}
			check("accepted")
			return nil
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := inspectInput(escaped, input); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired policy: %v", e)
	}
	if _, e := escapedReader.FingerprintDeleted("x"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired tombstone read: %v", e)
	}
	if _, e := escapedReader.ReconciliationRow("x", true); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired row read: %v", e)
	}
	if _, e := New(escapedReader, testOptions(t)); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired constructor: %v", e)
	}
	var publication *Policy
	if e = f.store.Read(ctx, func(r *assertions.Reader) error {
		var e error
		publication, e = New(r, testOptions(t))
		if e != nil {
			return e
		}
		result, e := inspectInput(publication, input)
		if e == nil && result.Status != "accepted" {
			t.Fatal(result)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := inspectInput(publication, input); e == nil {
		t.Fatal("escaped publication accepted")
	}
}
func TestSQLFailureIsNotSemanticRejection(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	if _, e := f.main.Exec("DROP TABLE memory_deleted"); e != nil {
		t.Fatal(e)
	}
	e := f.store.Read(ctx, func(reader *assertions.Reader) error {
		p, e := New(reader, testOptions(t))
		if e != nil {
			return e
		}
		r, e := p.Inspect(obj("sceneId", "s"), obj("content", "Synthetic"), obj("kind", "scene", "sceneId", "s"), obj(), nil)
		if e == nil || r.Status != "" {
			t.Fatalf("SQL failure disguised as decision: %+v %v", r, e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestEarlyRejectionsDoNotParseMalformedStoredRows(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	candidate := obj("content", "Synthetic")
	scope := obj("kind", "scene", "sceneId", "s")
	fp, e := assertions.Fingerprint(obj("content", "Synthetic", "scope", scope))
	if e != nil {
		t.Fatal(e)
	}
	e = f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
		if _, e := session.Exec("INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES('old','scene:s',NULL,'Synthetic',?,'{malformed')", fp); e != nil {
			return e
		}
		return f.store.WithSession(session).Read(ctx, func(reader *assertions.Reader) error {
			p, e := New(reader, testOptions(t))
			if e != nil {
				return e
			}
			invalid := obj("content", "Synthetic", "mergeIntoId", nil)
			r, e := p.Inspect(obj("sceneId", "s"), invalid, scope, obj(), nil)
			if e != nil || r.Reason != "invalid_merge_request" {
				t.Fatalf("invalid merge parsed corrupt preimage: %+v %v", r, e)
			}
			// Without either early rejection this exact stored preimage must fail.
			if r, e = p.Inspect(obj("sceneId", "s"), candidate, scope, obj(), nil); e == nil || r.Status != "" {
				t.Fatalf("malformed preimage was not read: %+v %v", r, e)
			}
			if _, e = session.Exec("INSERT INTO memory_deleted VALUES(?,'now')", fp); e != nil {
				return e
			}
			r, e = p.Inspect(obj("sceneId", "s"), candidate, scope, obj(), nil)
			if e != nil || r.Reason != "manually_deleted" {
				t.Fatalf("incoming tombstone parsed corrupt preimage: %+v %v", r, e)
			}
			return nil
		})
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestReadinessErrorsAtRecordAndRecalled(t *testing.T) {
	for _, site := range []string{"Record", "Recalled"} {
		t.Run(site, func(t *testing.T) {
			f := scopedSetup(t)
			ctx := context.Background()
			blocked := false
			calls := 0
			readiness := errors.New("synthetic readiness failure")
			store, e := assertions.New(f.main, f.recovery, assertions.Options{AssertReady: func() error {
				calls++
				if blocked {
					return readiness
				}
				return nil
			}})
			if e != nil {
				t.Fatal(e)
			}
			scope := obj("kind", "scene", "sceneId", "s")
			fp, e := assertions.Fingerprint(obj("scope", scope, "content", "Synthetic"))
			if e != nil {
				t.Fatal(e)
			}
			e = f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
				if _, e := session.Exec(`INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES('old','scene:s',NULL,'Synthetic',?,'{"id":"old","scope":{"kind":"scene","sceneId":"s"},"content":"Synthetic","kind":"claim","epistemicKind":"reported"}')`, fp); e != nil {
					return e
				}
				if _, e := session.Exec(`INSERT INTO memory_supports VALUES('old','[{"sources":[{"sceneId":"s","messageId":"m","senderId":"p"}]}]')`); e != nil {
					return e
				}
				return store.WithSession(session).Read(ctx, func(reader *assertions.Reader) error {
					p, e := New(reader, testOptions(t))
					if e != nil {
						return e
					}
					blocked = true
					calls = 0
					defer func() { blocked = false }()
					// Raw SQL reads check scope, not readiness; the policy must reach the
					// established Record or Recalled adapter before propagating this failure.
					if row, e := reader.ReconciliationRow(fp, false); e != nil || row == nil {
						t.Fatalf("raw row readiness changed: %v", e)
					}
					if _, e := reader.FingerprintDeleted(fp); e != nil {
						t.Fatal(e)
					}
					if calls != 0 {
						t.Fatal("raw reads added readiness")
					}
					attribution := obj()
					if site == "Record" {
						attribution = obj("witness", obj("kind", "mentioned"))
					}
					r, e := p.Inspect(obj("sceneId", "s"), obj("content", "Synthetic", "epistemicKind", "reported"), scope, attribution, nil)
					if !errors.Is(e, readiness) || r.Status != "" || calls != 1 {
						t.Fatalf("%s readiness: result=%+v error=%v calls=%d", site, r, e, calls)
					}
					return nil
				})
			})
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}
