package workingset

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type fixture struct {
	db        *sql.DB
	authority *recovery.Authority
	service   *Service
	now       float64
	enabled   bool
	snapshots []Snapshot
	tokens    int
}

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func setup(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	must(t, os.Mkdir(authorityPath, 0700))
	ledger, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	must(t, e)
	_, e = ledger.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	must(t, e)
	must(t, ledger.Close())
	must(t, os.Mkdir(filepath.Join(root, "live"), 0700))
	db, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite"))
	must(t, e)
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`)
	must(t, e)
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: 1, State: "ready"}
	raw, e := json.Marshal(gate)
	must(t, e)
	_, e = db.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw))
	must(t, e)
	a, e := recovery.OpenWriter(context.Background(), authorityPath)
	must(t, e)
	f := &fixture{db: db, authority: a, now: 10000, enabled: true}
	f.snapshots = []Snapshot{snap("a", "scene")}
	f.service, e = New(context.Background(), db, a, Ports{Enabled: func() bool { return f.enabled }, Now: func() float64 { return f.now }, Token: func() (string, error) { f.tokens++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", f.tokens), nil }, ResolveCurrent: func(_ *Scope, c Context, refs []Reference) ([]Snapshot, error) {
		out := []Snapshot{}
		for _, s := range f.snapshots {
			for _, r := range refs {
				if ReferenceKey(r) == ReferenceKey(s.Ref) {
					out = append(out, s)
					break
				}
			}
		}
		return out, nil
	}})
	must(t, e)
	t.Cleanup(func() { _ = db.Close(); _ = a.Close() })
	return f
}
func snap(id, scene string) Snapshot {
	now := 9000.0
	return Snapshot{Ref: Reference{Kind: "assertion", ID: id, Version: 1}, DependencyKey: strings.Repeat("a", 64), SceneID: scene, RecordedAt: &now}
}
func TestPinIntentSurvivesDerivedPrivacy(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := Context{SceneID: "scene"}
	selected := f.snapshots[0].Selection()
	p, e := f.service.SetPinned(ctx, c, selected, 0, true)
	must(t, e)
	if p.PolicyRevision != 1 {
		t.Fatal(p)
	}
	_, e = f.service.RecordSelected(ctx, c, SelectedInput{"turn", f.now, []Selection{selected}})
	must(t, e)
	f.enabled = false
	must(t, f.service.Invalidate(ctx, c.SceneID, []Entity{{"assertion", "a"}}))
	pins, e := f.service.ListPinPolicies(ctx, c, 100)
	must(t, e)
	if len(pins.Items) != 1 || !pins.Items[0].Pinned {
		t.Fatal(pins)
	}
	must(t, f.service.TerminalInvalidate(ctx, c.SceneID, []Entity{{"assertion", "a"}}))
	pins, e = f.service.ListPinPolicies(ctx, c, 100)
	must(t, e)
	if len(pins.Items) != 0 {
		t.Fatal(pins)
	}
	must(t, f.service.Transaction(ctx, func(s *Service) error {
		p, e := s.Storage().Pin("scene", Entity{"assertion", "a"})
		if e == nil && (p == nil || !p.Terminal || p.PolicyRevision != 2) {
			t.Fatal(p)
		}
		return e
	}))
}
func TestReceiptExpiryAndDedupAcrossDependency(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := Context{SceneID: "scene"}
	p := DefaultPolicy()
	p.MaxReceipts = 1
	p.ReceiptRetentionMs = 1000
	p.SelectionMaxAgeMs = 100
	f.service.policy.store(p)
	input := SelectedInput{"turn", f.now, []Selection{f.snapshots[0].Selection()}}
	a, e := f.service.RecordSelected(ctx, c, input)
	must(t, e)
	if a.Recorded != 1 {
		t.Fatal(a)
	}
	f.snapshots[0].DependencyKey = strings.Repeat("b", 64)
	input.Selections = []Selection{f.snapshots[0].Selection()}
	a, e = f.service.RecordSelected(ctx, c, input)
	must(t, e)
	if a.Duplicates != 1 {
		t.Fatal(a)
	}
	f.now += 1000
	input.SelectedAt = f.now
	input.TurnID = "second"
	a, e = f.service.RecordSelected(ctx, c, input)
	must(t, e)
	if a.Recorded != 0 || a.Coverage != "limited" {
		t.Fatal("strict expiry boundary", a)
	}
	f.now++
	input.SelectedAt = f.now
	a, e = f.service.RecordSelected(ctx, c, input)
	must(t, e)
	if a.Recorded != 1 {
		t.Fatal(a)
	}
}
func TestCompleteRechecksGenerationNotExpiryAfterCallback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := Context{SceneID: "scene"}
	selected := []Selection{f.snapshots[0].Selection()}
	_, e := f.service.Enqueue(ctx, c, selected)
	must(t, e)
	leases, e := f.service.Claim(ctx, c, 1)
	must(t, e)
	resolve := f.service.ports.ResolveCurrent
	f.service.ports.ResolveCurrent = func(s *Scope, c Context, r []Reference) ([]Snapshot, error) {
		f.now = leases[0].ExpiresAt + 1
		return resolve(s, c, r)
	}
	out, e := f.service.Complete(ctx, c, leases)
	must(t, e)
	if out.Completed != 1 {
		t.Fatal(out)
	}
}
func TestCompleteGenerationStolenInsideResolve(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := Context{SceneID: "scene"}
	_, e := f.service.Enqueue(ctx, c, []Selection{f.snapshots[0].Selection()})
	must(t, e)
	leases, e := f.service.Claim(ctx, c, 1)
	must(t, e)
	resolve := f.service.ports.ResolveCurrent
	f.service.ports.ResolveCurrent = func(scope *Scope, c Context, refs []Reference) ([]Snapshot, error) {
		store := f.service.WithSession(scope.WriteSession()).Storage()
		job, e := store.Job(c.SceneID, refs[0])
		if e != nil {
			return nil, e
		}
		job.Generation = "00000000-0000-4000-8000-000000000099"
		if e = store.PutJob(*job); e != nil {
			return nil, e
		}
		return resolve(scope, c, refs)
	}
	out, e := f.service.Complete(ctx, c, leases)
	must(t, e)
	if out.Completed != 0 || out.Stale != 1 {
		t.Fatal(out)
	}
}
func TestTerminalCleanupTraversesEveryMetadataPage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i := 0; i < 85; i++ {
		scene := fmt.Sprintf("scene-%03d", i)
		f.snapshots = []Snapshot{snap("a", scene)}
		_, e := f.service.SetPinned(ctx, Context{SceneID: scene}, f.snapshots[0].Selection(), 0, true)
		must(t, e)
	}
	must(t, f.service.Transaction(ctx, func(s *Service) error {
		out, e := s.Mutations(MutationPorts{}).TerminalAssertionDeleted("a")
		if e == nil && out.Scenes != 85 {
			t.Fatal(out)
		}
		return e
	}))
	var count int
	must(t, f.db.QueryRow("SELECT count(*) FROM memory_working_set_pins WHERE json_extract(value,'$.terminal')=1").Scan(&count))
	if count != 85 {
		t.Fatal(count)
	}
}
func TestHeldCallbacksAndOuterRollbackLeaveDurableGap(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := Context{SceneID: "scene"}
	var held *recovery.WriteSession
	f.service.ports.ObservePinWrite = func(s *recovery.WriteSession, _ PinPolicy) error { held = s; return nil }
	rollback := errors.New("rollback")
	e := f.service.Transaction(ctx, func(s *Service) error {
		_, e := s.SetPinned(ctx, c, f.snapshots[0].Selection(), 0, true)
		if e != nil {
			return e
		}
		return rollback
	})
	if !errors.Is(e, rollback) {
		t.Fatal(e)
	}
	if held == nil {
		t.Fatal("missing callback")
	}
	if _, e = held.Exec("SELECT 1"); e == nil {
		t.Fatal("held capability alive")
	}
	_, e = f.service.ListPinPolicies(ctx, c, 100)
	if e == nil {
		t.Fatal("durable intent / rolled-back ACK must fail closed")
	}
	var n int
	must(t, f.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='memory_working_set_pins'").Scan(&n))
	if n != 0 {
		t.Fatal("schema escaped rollback")
	}
}
func TestNestedDerivedRollbackKeepsOuterPrefix(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	rollback := errors.New("rollback")
	must(t, f.service.Transaction(ctx, func(s *Service) error {
		if e := s.write(); e != nil {
			return e
		}
		e := s.Transaction(ctx, func(child *Service) error {
			_, e := child.Enqueue(ctx, Context{SceneID: "scene"}, []Selection{f.snapshots[0].Selection()})
			if e != nil {
				return e
			}
			return rollback
		})
		if !errors.Is(e, rollback) {
			return e
		}
		n, e := s.Storage().Pending("scene")
		if n != 0 {
			t.Fatal(n)
		}
		return e
	}))
}
func TestNativeTransactionLossLatches(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	must(t, f.service.Transaction(ctx, func(s *Service) error { return s.write() }))
	_, e := f.db.Exec(`CREATE TRIGGER lose_workingset BEFORE INSERT ON memory_working_set_jobs BEGIN SELECT RAISE(ROLLBACK,'lost'); END`)
	must(t, e)
	e = f.service.Transaction(ctx, func(s *Service) error {
		_, _ = s.Enqueue(ctx, Context{SceneID: "scene"}, []Selection{f.snapshots[0].Selection()})
		_, after := s.session.Exec("INSERT INTO memory_working_set_receipts VALUES('bad',1,'{}')")
		if after == nil {
			t.Fatal("autocommit after native loss")
		}
		return nil
	})
	if e == nil {
		t.Fatal("lost transaction accepted")
	}
}
func TestLosslessSchemaOrderAndLengths(t *testing.T) {
	raw := `{"deletionEpoch":0,"version":1,"id":"\ud800","kind":"assertion"}`
	r, e := Decode[Reference](raw)
	must(t, e)
	actual, e := Encode(r)
	must(t, e)
	if actual != `{"kind":"assertion","id":"\ud800","version":1,"deletionEpoch":0}` {
		t.Fatal(actual)
	}
	r.ID = strings.Repeat("😀", 512)
	must(t, ValidateReference(r))
	if explicitID(r.ID) {
		t.Fatal("UTF16 distinct from schema codepoints")
	}
	if n, _ := jsjson.CodePointCount(r.ID); n != 512 {
		t.Fatal(n)
	}
}

type fakeScheduler struct{ tasks []*fakeTimer }
type fakeTimer struct {
	fn       func()
	canceled bool
	delay    time.Duration
}

func (f *fakeScheduler) Schedule(d time.Duration, fn func()) CancelTimer {
	t := &fakeTimer{fn: fn, delay: d}
	f.tasks = append(f.tasks, t)
	return t
}
func (f *fakeTimer) Cancel() { f.canceled = true }
func (f *fakeScheduler) tick() {
	t := f.tasks[0]
	f.tasks = f.tasks[1:]
	if !t.canceled {
		t.fn()
	}
}
func TestMaintenanceNotificationTimerAndDisposal(t *testing.T) {
	scheduler := &fakeScheduler{}
	enabled := true
	drains := 0
	fail := false
	failures := 0
	var m *Maintenance
	m, e := NewMaintenance(MaintenancePorts{Enabled: func() (bool, error) { return enabled, nil }, DrainOneBatch: func() (bool, error) {
		drains++
		if fail {
			return false, errors.New("fail")
		}
		if drains == 2 {
			m.Dispose()
		}
		return true, nil
	}, Failed: func() { failures++; panic("report") }}, 250, scheduler)
	must(t, e)
	if len(scheduler.tasks) != 0 {
		t.Fatal("constructor scheduled")
	}
	m.Start()
	m.Notify()
	if len(scheduler.tasks) != 1 || scheduler.tasks[0].delay != 250*time.Millisecond {
		t.Fatal("not one timer")
	}
	scheduler.tick()
	if drains != 1 || len(scheduler.tasks) != 1 {
		t.Fatal(drains)
	}
	scheduler.tick()
	if drains != 2 || len(scheduler.tasks) != 0 {
		t.Fatal("dispose resurrected")
	}
	m.Start()
	fail = true
	scheduler.tick()
	if failures != 1 || len(scheduler.tasks) != 0 {
		t.Fatal("error resurrected")
	}
	enabled = false
	m.Configure()
	if len(scheduler.tasks) != 0 {
		t.Fatal("disabled")
	}
	enabled = true
	m.Notify()
	m.Cancel()
	scheduler.tick()
	if drains != 3 {
		t.Fatal(drains)
	}
}

func TestPublicationReadsDoNotReserveMainWriter(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := Context{SceneID: "scene"}
	selected := f.snapshots[0].Selection()
	_, e := f.service.SetPinned(ctx, c, selected, 0, true)
	must(t, e)
	f.db.SetMaxOpenConns(2)
	writer, e := f.db.Conn(ctx)
	must(t, e)
	defer writer.Close()
	_, e = writer.ExecContext(ctx, "BEGIN IMMEDIATE")
	must(t, e)
	defer writer.ExecContext(ctx, "ROLLBACK")
	_, e = writer.ExecContext(ctx, "DELETE FROM memory_working_set_pins")
	must(t, e)
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	view, e := f.service.View(deadline, c, []Selection{selected}, true)
	must(t, e)
	if len(view.Items) != 1 || !view.Items[0].Pinned {
		t.Fatal("did not use committed WAL snapshot", view)
	}
	pins, e := f.service.ListPinPolicies(deadline, c, 100)
	must(t, e)
	if len(pins.Items) != 1 {
		t.Fatal(pins)
	}
}
func TestPublicationScopeDenialsAndExpiration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	must(t, f.service.Transaction(ctx, func(s *Service) error { return s.write() }))
	_, e := f.db.Exec(`CREATE TEMP TABLE memory_working_set_pins(scene_id,kind,entity_id,value);ATTACH ":memory:" AS elsewhere;CREATE TABLE elsewhere.memory_working_set_pins(scene_id,kind,entity_id,value)`)
	must(t, e)
	var held *Scope
	var rows Rows
	var heldStore *Storage
	must(t, f.service.Read(ctx, func(s *Service) error {
		held = s.scope()
		heldStore = s.Storage()
		if held.WriteSession() != nil {
			t.Fatal("fabricated writer")
		}
		for _, q := range []string{"INSERT INTO memory_working_set_receipts VALUES('x',1,'{}') RETURNING receipt_key", "CREATE TABLE escaped(v)", "COMMIT", "ROLLBACK", "SAVEPOINT escaped", "ATTACH ':memory:' AS escaped", "PRAGMA user_version=1", "PRAGMA table_info(memory_working_set_pins)", "PRAGMA temp.table_info(memory_working_set_pins)", "PRAGMA elsewhere.table_info(memory_working_set_pins)", "SELECT * FROM temp.memory_working_set_pins", "SELECT * FROM elsewhere.memory_working_set_pins"} {
			r, e := held.Query(q)
			if e == nil {
				r.Close()
				t.Fatalf("read capability allowed %s", q)
			}
		}
		if e := heldStore.PutReceipt("x", Receipt{1, "scene", "turn", f.snapshots[0].Selection(), f.now}); e == nil {
			t.Fatal("read storage wrote")
		}
		var e error
		rows, e = held.Query("SELECT scene_id FROM main.memory_working_set_pins")
		return e
	}))
	if e := held.CheckScope(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	if _, e := held.Query("SELECT 1"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	if rows.Next() {
		t.Fatal("held rows remained alive")
	}
	if !errors.Is(rows.Err(), sql.ErrTxDone) {
		t.Fatal(rows.Err())
	}
	if _, e := heldStore.Exists(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	if _, e := f.service.Storage().Exists(); e == nil {
		t.Fatal("unbound storage capability")
	}
	if e := f.service.Storage().Ensure(); e == nil {
		t.Fatal("unbound write capability")
	}
}
func TestBoundReadBorrowsExactChildAndUncommittedMetadata(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := Context{SceneID: "scene"}
	must(t, f.service.Transaction(ctx, func(s *Service) error {
		must(t, s.write())
		must(t, s.Storage().PutItem(makeItem(f.snapshots[0], nil, f.now)))
		expected := s.session
		resolve := s.ports.ResolveCurrent
		s.ports.ResolveCurrent = func(scope *Scope, c Context, refs []Reference) ([]Snapshot, error) {
			if scope.WriteSession() != expected {
				t.Fatal("read opened another savepoint")
			}
			if _, e := expected.Exec("SELECT 1"); e != nil {
				t.Fatal("parent suspended", e)
			}
			return resolve(scope, c, refs)
		}
		view, e := s.View(ctx, c, []Selection{f.snapshots[0].Selection()}, true)
		if e != nil {
			return e
		}
		if len(view.Items) != 1 || view.Items[0].MetadataRevision != 1 {
			t.Fatal(view)
		}
		return nil
	}))
}

func TestConcurrentPolicyConfigurationAndReads(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.db.SetMaxOpenConns(3)
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if worker == 0 {
					cold := float64(1000 + i)
					if e := f.service.ConfigurePolicy(&cold, nil); e != nil {
						errs <- e
						return
					}
				} else if worker == 1 {
					review := float64(500 + i)
					if e := f.service.ConfigurePolicy(nil, &review); e != nil {
						errs <- e
						return
					}
				} else {
					if _, e := f.service.View(ctx, Context{SceneID: "scene"}, []Selection{f.snapshots[0].Selection()}, true); e != nil {
						errs <- e
						return
					}
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		must(t, e)
	}
	p := f.service.policy.load()
	if p.ColdAfterMs != 1019 || p.ReviewAfterMs != 519 {
		t.Fatal("lost incremental update", p)
	}
	// Reconfiguration from an eligibility callback must not deadlock or be
	// hidden by a lock spanning the operation. Classification uses the new value.
	resolve := f.service.ports.ResolveCurrent
	f.service.ports.ResolveCurrent = func(scope *Scope, c Context, refs []Reference) ([]Snapshot, error) {
		cold := float64(900)
		if e := f.service.ConfigurePolicy(&cold, nil); e != nil {
			return nil, e
		}
		return resolve(scope, c, refs)
	}
	_, e := f.service.View(ctx, Context{SceneID: "scene"}, []Selection{f.snapshots[0].Selection()}, true)
	must(t, e)
}
