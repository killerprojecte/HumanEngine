package memoryworkspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	_ "github.com/mattn/go-sqlite3"
)

type scopedFixture struct {
	main     *sql.DB
	recovery *recovery.Authority
	store    *assertions.Store
	events   *sourceevents.Store
}

func scopedSetup(t *testing.T) scopedFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	if e := os.Mkdir(authorityPath, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(root, "live"), 0700); e != nil {
		t.Fatal(e)
	}
	main, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite")+"?_foreign_keys=on")
	if e != nil {
		t.Fatal(e)
	}
	main.SetMaxOpenConns(1)
	_, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id));INSERT INTO scenes VALUES('s','{"id":"s","adapterId":"adapter","accountId":"account"}')`)
	if e != nil {
		t.Fatal(e)
	}
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: 1, State: "ready"}
	raw, _ := json.Marshal(gate)
	if _, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	ra, e := recovery.OpenWriter(ctx, authorityPath)
	if e != nil {
		t.Fatal(e)
	}
	store, e := assertions.New(main, ra, assertions.Options{})
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	events, e := sourceevents.New(main, ra, sourceevents.Options{ID: func() string { return "source-event" }, Now: func() string { return "2026-10-28T12:00:00.000Z" }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ra.Close(); main.Close() })
	return scopedFixture{main, ra, store, events}
}
func parseObject(t *testing.T, raw string) *assertions.Object {
	t.Helper()
	v, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v.(*assertions.Object)
}
func testOptions(t *testing.T) assertionauthority.Options {
	t.Helper()
	p, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	return assertionauthority.Options{DateParser: p, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }, NowMillis: func() int64 { return 1793188800000 }}
}

func TestResolverActualAuthorityOneConnectionAndScope(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	options := testOptions(t)
	plans, e := commitments.New(f.main, f.recovery, commitments.Options{Compare: options.Compare, DateParser: options.DateParser})
	if e != nil {
		t.Fatal(e)
	}
	if e = plans.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	record := parseObject(t, `{"id":"a","scope":{"kind":"scene","sceneId":"s","personId":"p"},"kind":"experience","content":"Synthetic a","sources":[],"status":"active","learnedAt":"2000-01-01T00:00:00.000Z"}`)
	if _, e = f.main.Exec("INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES('a','scene:s','p','Synthetic a',?,'a')", stringify(t, record)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.main.Exec("INSERT INTO memory_assertion_state VALUES('a',1,0,0)"); e != nil {
		t.Fatal(e)
	}
	refs := []workingset.Reference{{Kind: "assertion", ID: "a", Version: 1}}
	enabled := true
	flag := func(commitments.Context) (bool, error) { return enabled, nil }
	var escaped *Resolver
	check := func(read *commitments.ReadService, want int, protection string) error {
		resolver, e := NewResolver(read, options.DateParser)
		if e != nil {
			return e
		}
		escaped = resolver
		values, e := resolver.Resolve(workingset.Context{SceneID: "s"}, refs)
		if e != nil {
			return e
		}
		if len(values) != want {
			t.Fatalf("got %v want %d", values, want)
		}
		if want > 0 {
			got := values[0]
			if got.Ref.ID != "a" || got.PersonID == nil || *got.PersonID != "p" || got.RecordedAt == nil || *got.RecordedAt != 946684800000 {
				t.Fatalf("wrong snapshot %+v", got)
			}
			if protection == "" && got.Protection != nil || protection != "" && (got.Protection == nil || *got.Protection != protection) {
				t.Fatalf("protection %+v want %s", got, protection)
			}
		}
		return nil
	}
	if e = plans.ReadCurrent(ctx, f.store, flag, options, func(read *commitments.ReadService) error { return check(read, 1, "") }); e != nil {
		t.Fatal(e)
	}
	if _, e = escaped.Resolve(workingset.Context{Simulation: true}, nil); e == nil {
		t.Fatal("expired resolver accepted simulation")
	}
	head := parseObject(t, `{"id":"p","sceneId":"s","latestVersion":1,"activeVersion":null,"everActivatedVersion":null,"everTerminal":null,"deletionEpoch":0,"mutationVersion":1,"deleted":false}`)
	if e = plans.Transaction(ctx, func(s *commitments.Store) error { return s.Put(head, nil) }); e != nil {
		t.Fatal(e)
	}
	enabled = false
	if e = plans.ReadCurrent(ctx, f.store, flag, options, func(read *commitments.ReadService) error { return check(read, 1, "unresolved_commitment_coverage") }); e != nil {
		t.Fatal(e)
	}
	if e = f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
		return plans.WithSession(session).ReadCurrent(ctx, f.store.WithSession(session), flag, options, func(read *commitments.ReadService) error {
			if e := check(read, 1, "unresolved_commitment_coverage"); e != nil {
				return e
			}
			if _, e := session.Exec("UPDATE memories SET value=json_set(value,'$.status','invalidated') WHERE id='a'"); e != nil {
				return e
			}
			return check(read, 0, "")
		})
	}); e != nil {
		t.Fatal(e)
	}
}

// These cases use real source/assertion/commitment stores on the fixture's single
// connection. No substitute workspaceRead or qualification callback is used.
func seedSupportedAssertion(t *testing.T, f scopedFixture, eventRef *sourceevents.Ref) {
	t.Helper()
	ctx := context.Background()
	options := testOptions(t)
	coordinator, e := assertions.NewCoordinator(f.store, assertions.CoordinatorOptions{
		HistorySupports: func(s *assertions.Store, id string) (proofs []any, err error) {
			err = s.Read(ctx, func(r *assertions.Reader) error {
				a, e := assertionauthority.New(r, options)
				if e != nil {
					return e
				}
				proofs, e = a.HistorySupports(id)
				return e
			})
			return
		},
		Retain:           func(*assertions.Store, *Object) (bool, error) { return true, nil },
		RemoveProjection: func(*assertions.Store, string) error { return nil },
		Committed:        func(*assertions.Store, string, int64, int64) error { return nil },
	})
	if e != nil {
		t.Fatal(e)
	}
	source := obj("sceneId", "s", "messageId", "m", "senderId", "p")
	if eventRef != nil {
		_ = source.Set("eventRef", eventRef.Value())
	}
	proof := obj("sources", []any{source}, "parents", []any{}, "parentReferences", []any{}, "subjectWitness", obj("kind", "self", "personId", "p", "sourceMessageId", "m"))
	record := parseObject(t, `{"id":"a","scope":{"kind":"scene","sceneId":"s","personId":"p"},"kind":"fact","epistemicKind":"explicit","content":"Synthetic supported declaration","sources":[],"importance":"low","confidence":0.5,"learnedAt":"2026-10-28T00:00:00.000Z","status":"active","locked":false,"origin":"learned","lineageStatus":"verified"}`)
	_ = record.Set("sources", []any{source})
	if e = coordinator.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		if e := tx.Repository.Write(record); e != nil {
			return e
		}
		return tx.Repository.SaveSupports("a", []any{proof})
	}); e != nil {
		t.Fatal(e)
	}
}

func nativePlans(t *testing.T, f scopedFixture) *commitments.Store {
	t.Helper()
	options := testOptions(t)
	plans, e := commitments.New(f.main, f.recovery, commitments.Options{Compare: options.Compare, DateParser: options.DateParser})
	if e != nil {
		t.Fatal(e)
	}
	if e = plans.Initialize(context.Background()); e != nil {
		t.Fatal(e)
	}
	return plans
}

func nativeIngress() sourceevents.Change {
	return sourceevents.Change{Kind: "ingress", Identity: sourceevents.Identity{SceneID: "s", AdapterID: "adapter", AccountID: "account", MessageID: "m", SenderID: "p"}, BodyHash: strings.Repeat("a", 64)}
}

func nativeDigest(t *testing.T, value any) string {
	t.Helper()
	return fmt.Sprintf("%x", sha256.Sum256([]byte(stringify(t, value))))
}

func TestResolverRegisteredSourceHashEditAndRecall(t *testing.T) {
	for _, mutation := range []string{"edit", "recall"} {
		t.Run(mutation, func(t *testing.T) {
			f := scopedSetup(t)
			plans := nativePlans(t, f)
			seedSupportedAssertion(t, f, nil)
			ctx := context.Background()
			options := testOptions(t)
			var before, registered string
			check := func(want bool, registeredHead bool) string {
				t.Helper()
				var key string
				e := plans.ReadCurrent(ctx, f.store, func(commitments.Context) (bool, error) { return true, nil }, options, func(read *commitments.ReadService) error {
					r, e := NewResolver(read, options.DateParser)
					if e != nil {
						return e
					}
					values, e := r.Resolve(workingset.Context{SceneID: "s"}, []workingset.Reference{{Kind: "assertion", ID: "a", Version: 1}})
					if e != nil {
						return e
					}
					if !want {
						if len(values) != 0 {
							t.Fatalf("%s retained stale assertion: %+v", mutation, values)
						}
						return nil
					}
					if len(values) != 1 || values[0].Protection != nil {
						t.Fatalf("unexpected assertion: %+v", values)
					}
					view, e := read.CurrentAssertion(commitments.Context{SceneID: "s"}, commitments.Reference{ID: "a", Version: 1})
					if e != nil {
						return e
					}
					head, e := read.SourceHead("s", "m")
					if e != nil {
						return e
					}
					if (head != nil) != registeredHead || view == nil || len(view.Proofs) != 1 {
						t.Fatalf("missing actual source/proof: %v %+v", head, view)
					}
					var metadata any
					if head != nil {
						metadata = head
					}
					authority := nativeDigest(t, obj("record", view.Record, "proofs", view.Proofs, "heads", []any{obj("sceneId", "s", "messageId", "m", "head", metadata)}))
					wantKey := nativeDigest(t, obj("authority", authority, "plans", []any{}, "protectionUnknown", false))
					key = values[0].DependencyKey
					if key != wantKey {
						t.Fatalf("source metadata dependency hash %s != %s", key, wantKey)
					}
					return nil
				})
				if e != nil {
					t.Fatal(e)
				}
				return key
			}
			before = check(true, false)
			change := nativeIngress()
			if _, e := f.events.Observe(ctx, change); e != nil {
				t.Fatal(e)
			}
			registered = check(true, true)
			if before == registered || check(true, true) != registered {
				t.Fatal("registration did not change the hash, or unchanged read was unstable")
			}
			if mutation == "edit" {
				change.Kind, change.EditID, change.BodyHash = "edit", "edit-1", strings.Repeat("b", 64)
				change.Order = sourceevents.Order{Kind: "platform_sequence", Sequence: 1}
				if _, e := f.events.Observe(ctx, change); e != nil {
					t.Fatal(e)
				}
			} else if e := f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
				if _, e := session.AppendIntent(recovery.Source{SceneID: "s", MessageID: "m"}); e != nil {
					return e
				}
				if _, e := session.Exec("INSERT INTO recall_tombstones VALUES('s','m','2026-10-28T13:00:00.000Z')"); e != nil {
					return e
				}
				return f.events.WithSession(session).RevokeAll(ctx, "s", "m")
			}); e != nil {
				t.Fatal(e)
			}
			check(false, true)
		})
	}
}

func TestResolverEnabledActualCommitmentProtectionAndHash(t *testing.T) {
	f := scopedSetup(t)
	plans := nativePlans(t, f)
	ctx := context.Background()
	options := testOptions(t)
	observed, e := f.events.Observe(ctx, nativeIngress())
	if e != nil {
		t.Fatal(e)
	}
	if observed.Ref == nil {
		t.Fatal("ingress did not register a source revision")
	}
	seedSupportedAssertion(t, f, observed.Ref)
	enabled := func(commitments.Context) (bool, error) { return true, nil }
	var evidence *Object
	if e = plans.ReadCurrent(ctx, f.store, enabled, options, func(read *commitments.ReadService) error {
		view, e := read.CurrentAssertion(commitments.Context{SceneID: "s"}, commitments.Reference{ID: "a", Version: 1})
		if e != nil {
			return e
		}
		if view == nil || len(view.Proofs) != 1 {
			t.Fatal("registered source did not qualify actual assertion")
		}
		fingerprint, e := commitments.BackingFingerprint(view.Record)
		if e != nil {
			return e
		}
		evidence = obj("backing", obj("id", "a", "version", float64(1), "deletionEpoch", float64(0)), "backingFingerprint", fingerprint, "proofs", clone(view.Proofs), "personId", "p", "kind", "proposal")
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	head := parseObject(t, `{"id":"plan","sceneId":"s","latestVersion":1,"activeVersion":null,"everActivatedVersion":null,"everTerminal":null,"deletionEpoch":0,"mutationVersion":1,"deleted":false}`)
	revision := parseObject(t, `{"ref":{"id":"plan","version":1,"deletionEpoch":0},"sceneId":"s","terms":{"kind":"plan","title":"Synthetic plan","participantIds":["p"],"temporalScope":"future","polarity":"positive","modality":"asserted","conditions":[]},"recordedAt":"2026-10-28T00:00:00.000Z","evidence":[],"requiredProposalGroups":[]}`)
	_ = revision.Set("evidence", []any{evidence})
	// The same registered source occurs in both evidence and a required proposal
	// group. Both occurrences must remain in hashes and assertion dependencies.
	_ = revision.Set("requiredProposalGroups", []any{[]any{clone(evidence)}})
	put := func() {
		t.Helper()
		if e := plans.Transaction(ctx, func(s *commitments.Store) error { return s.Put(head, revision) }); e != nil {
			t.Fatal(e)
		}
	}
	put()
	var previousAssertion, previousPlan string
	for _, stage := range []struct {
		mutation   float64
		protection string
	}{{1, "open_commitment"}, {2, "needs_review_commitment"}} {
		if stage.mutation == 2 {
			_ = head.Set("mutationVersion", stage.mutation)
			_ = head.Set("activeVersion", float64(1))
			_ = head.Set("everActivatedVersion", float64(1))
			_ = head.Set("everTerminal", obj("version", float64(1), "state", "done"))
			put()
		}
		if e = plans.ReadCurrent(ctx, f.store, enabled, options, func(read *commitments.ReadService) error {
			r, e := NewResolver(read, options.DateParser)
			if e != nil {
				return e
			}
			mutation := stage.mutation
			refs := []workingset.Reference{{Kind: "commitment", ID: "plan", Version: 1, MutationVersion: &mutation}, {Kind: "assertion", ID: "a", Version: 1}}
			values, e := r.Resolve(workingset.Context{SceneID: "s"}, refs)
			if e != nil {
				return e
			}
			if len(values) != 2 || values[0].Ref.Kind != "assertion" || values[1].Ref.Kind != "commitment" {
				t.Fatalf("unexpected native result ordering: %+v", values)
			}
			for _, value := range values {
				if value.Protection == nil || *value.Protection != stage.protection || value.RecordedAt == nil || *value.RecordedAt != 1793145600000 {
					t.Fatalf("incorrect actual plan protection/time: %+v", value)
				}
			}
			view, e := read.Current(commitments.Context{SceneID: "s"}, commitments.Reference{ID: "plan", Version: 1})
			if e != nil {
				return e
			}
			assertion, e := read.CurrentAssertion(commitments.Context{SceneID: "s"}, commitments.Reference{ID: "a", Version: 1})
			if e != nil {
				return e
			}
			source, e := read.SourceHead("s", "m")
			if e != nil {
				return e
			}
			if source == nil || assertion == nil || view == nil || len(arr(get(view, "evidence"))) != 1 || len(arr(get(view, "requiredProposalGroups"))) != 1 {
				t.Fatal("actual current view lost registered evidence")
			}
			planKey := nativeDigest(t, obj("view", view, "heads", []any{source, source}))
			authorityKey := nativeDigest(t, obj("record", assertion.Record, "proofs", assertion.Proofs, "heads", []any{obj("sceneId", "s", "messageId", "m", "head", source)}))
			dependency := obj("ref", get(view, "ref"), "mutationVersion", mutation, "lifecycle", get(view, "lifecycle"), "qualification", planKey)
			assertionKey := nativeDigest(t, obj("authority", authorityKey, "plans", []any{dependency, dependency}, "protectionUnknown", false))
			if values[0].DependencyKey != assertionKey || values[1].DependencyKey != planKey {
				t.Fatal("actual plan or protected assertion hash omitted source/dependency occurrences")
			}
			if values[1].Ref.MutationVersion == nil || *values[1].Ref.MutationVersion != mutation {
				t.Fatal("current mutation anchor missing")
			}
			if previousAssertion == assertionKey || previousPlan == planKey {
				t.Fatal("plan mutation/lifecycle did not invalidate both dependency hashes")
			}
			previousAssertion, previousPlan = assertionKey, planKey
			stale := mutation + 1
			refs[0].MutationVersion = &stale
			values, e = r.Resolve(workingset.Context{SceneID: "s"}, refs)
			if e != nil {
				return e
			}
			if len(values) != 1 || values[0].DependencyKey != assertionKey {
				t.Fatal("stale plan anchor survived or removed actual assertion protection")
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
	// Editing the registered revision revokes the exact evidence, even though
	// the commitment head itself still exists at the requested mutation anchor.
	change := nativeIngress()
	change.Kind, change.EditID, change.BodyHash = "edit", "edit-1", strings.Repeat("b", 64)
	change.Order = sourceevents.Order{Kind: "platform_sequence", Sequence: 1}
	if _, e = f.events.Observe(ctx, change); e != nil {
		t.Fatal(e)
	}
	if e = plans.ReadCurrent(ctx, f.store, enabled, options, func(read *commitments.ReadService) error {
		r, e := NewResolver(read, options.DateParser)
		if e != nil {
			return e
		}
		mutation := float64(2)
		values, e := r.Resolve(workingset.Context{SceneID: "s"}, []workingset.Reference{{Kind: "assertion", ID: "a", Version: 1}, {Kind: "commitment", ID: "plan", Version: 1, MutationVersion: &mutation}})
		if e != nil {
			return e
		}
		if len(values) != 1 || values[0].Ref.Kind != "commitment" || values[0].RecordedAt != nil || values[0].DependencyKey == previousPlan {
			t.Fatalf("edited evidence remained in workspace: %+v", values)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
