package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func semanticOwnerSetup(t *testing.T) (*SemanticRuntime, scopedFixture, []Reference, *modelread.Policy) {
	t.Helper()
	base, f := serviceSetup(t)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	settingsValue, e := jsjson.Parse(string(contract.DefaultSettingsInput()))
	must(e)
	settings := settingsValue.(*Object)
	for _, key := range []string{"memorySemanticEnabled", "memorySemanticAutoAdmissionEnabled", "memoryKnowledgeEnabled"} {
		must(settings.Set(key, true))
	}
	settingsJSON, e := jsjson.Stringify(settings)
	must(e)
	_, e = f.main.Exec(`CREATE TABLE IF NOT EXISTS config(key TEXT PRIMARY KEY,value TEXT NOT NULL)`)
	must(e)
	_, e = f.main.Exec("INSERT OR REPLACE INTO config VALUES('settings',?)", settingsJSON)
	must(e)
	_, e = f.main.Exec(`UPDATE scenes SET value='{"id":"s","adapterId":"adapter","accountId":"account","learningEnabled":true}' WHERE id='s'`)
	must(e)
	records := []*Object{}
	for _, id := range []string{"a", "b"} {
		record := putRecord(t, f, id, `,"origin":"learned","kind":"fact","epistemicKind":"explicit","lineageStatus":"verified"`)
		source := epObj("sceneId", "s", "messageId", "message-"+id, "senderId", "p")
		_ = record.Set("sources", []any{source})
		raw, e := jsjson.Stringify(record)
		must(e)
		_, e = f.main.Exec("UPDATE memories SET value=? WHERE id=?", raw, id)
		must(e)
		supports := []any{epObj("sources", []any{source}, "parents", []any{}, "parentReferences", []any{}, "subjectWitness", epObj("kind", "self", "personId", "p", "sourceMessageId", "message-"+id))}
		raw, e = jsjson.Stringify(supports)
		must(e)
		_, e = f.main.Exec("INSERT INTO memory_supports(memory_id,value) VALUES(?,?)", id, raw)
		must(e)
		_, e = f.main.Exec("INSERT OR IGNORE INTO memory_sources VALUES(?,?,?,?)", id, "s", "message-"+id, "p")
		must(e)
		records = append(records, record)
	}
	refs, e := base.CaptureParentReferences(context.Background(), Context{SceneID: "s"}, records)
	must(e)
	if len(refs) != 2 {
		t.Fatal(refs)
	}
	read, e := modelread.New(f.store)
	must(e)
	logs := observability.New(io.Discard, io.Discard)
	t.Cleanup(func() { logs.Close(context.Background()) })
	maintenance, e := workingset.NewMaintenance(workingset.MaintenancePorts{Enabled: func() (bool, error) { return true, nil }, DrainOneBatch: func() (bool, error) {
		result, e := base.d.WorkingSet.Drain(context.Background(), workingset.Context{SceneID: "s"}, 40)
		return result.Pending > 0, e
	}}, 1000, nil)
	must(e)
	t.Cleanup(maintenance.Dispose)
	options := semantic.DefaultCoordinatorOptions()
	options.Collator, e = topiccollation.New("en-US")
	if errors.Is(e, topiccollation.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
		t.Skip("native ICU profile required for concrete semantic owner")
	}
	must(e)
	t.Cleanup(func() { options.Collator.Close() })
	runtime, e := NewSemanticRuntime(SemanticRuntimeDependencies{Memory: base.d, Sources: f.events, Main: f.main, Authority: f.recovery, Models: &providertransport.NativeModels{}, Read: read, Foreground: &botruntime.Runtime{}, Logs: logs, Maintenance: maintenance, Options: options})
	must(e)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		must(runtime.CloseAndWait(ctx))
	})
	return runtime, f, refs, read
}
func semanticOwnerSnapshot(t *testing.T, r *SemanticRuntime, refs []Reference, read *modelread.Policy) *semantic.Input {
	t.Helper()
	ctx := context.Background()
	a, e := r.Memory.SemanticCurrent(ctx, Context{SceneID: "s"}, refs[0])
	if e != nil || a == nil {
		t.Fatal(a, e)
	}
	ticket, e := read.Require(ctx, "s")
	if e != nil {
		t.Fatal(e)
	}
	person := "p"
	resolved, e := r.ResolveDirty(ctx, []semantic.Dirty{{SceneID: "s", PersonID: &person, Trigger: epObj("ref", refObject(refs[0]), "dependencyKey", value(a, "dependencyKey")), Reason: "assertion_committed"}}, ticket)
	if e != nil || resolved == nil {
		t.Fatal(resolved, e)
	}
	snapshot := semantic.CaptureInput("s", *resolved)
	if snapshot == nil {
		t.Fatal("no complete capture", resolved)
	}
	return snapshot
}
func TestConcreteSemanticOwnerQualificationReceiptAndCandidate(t *testing.T) {
	r, f, refs, read := semanticOwnerSetup(t)
	ctx := context.Background()
	snapshot := semanticOwnerSnapshot(t, r, refs, read)
	if !r.Valid(ctx, snapshot) {
		t.Fatal("snapshot invalid")
	}
	started, e := r.BeginAttempt(ctx, snapshot)
	if e != nil || !started {
		t.Fatal(started, e)
	}
	started, e = r.BeginAttempt(ctx, snapshot)
	if e != nil || started {
		t.Fatal("replayed attempt", started, e)
	}
	proposal := epObj("kind", "summary_candidate", "selectedParentIds", []any{"a", "b"}, "text", "Synthetic attributed summary", "hypothesis", false)
	guards := 0
	result, e := r.Commit(ctx, snapshot, []*Object{proposal}, func(ws *recovery.WriteSession) error {
		guards++
		if !r.PolicyInSession(ctx, "s", 0, ws) || !r.ValidInSession(ctx, snapshot, ws) {
			return errors.New("real writer guard failed")
		}
		return nil
	})
	if e != nil || result.Created != 1 || guards != 1 {
		t.Fatal(result, e, guards)
	}
	if e = r.FinishAttempt(ctx, snapshot, "finished"); e != nil {
		t.Fatal(e)
	}
	if e = f.recovery.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		rows, e := r.Memory.d.Semantic.WithSession(ws).Rows(nil)
		if e != nil {
			return e
		}
		if len(rows) != 1 || value(rows[0], "status") != "admitted" || value(rows[0], "admission") != "structural_low_risk_summary" {
			t.Fatal(rows)
		}
		attempts, e := r.Memory.d.Semantic.WithSession(ws).Attempts()
		if e != nil {
			return e
		}
		if len(attempts) != 1 || value(attempts[0], "status") != "finished" || value(attempts[0], "version") != float64(2) {
			t.Fatal(attempts)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	_, e = f.main.Exec(`UPDATE scenes SET value='{"id":"s","learningEnabled":false}' WHERE id='s'`)
	if e != nil {
		t.Fatal(e)
	}
	result, e = r.Commit(ctx, snapshot, []*Object{proposal}, func(ws *recovery.WriteSession) error {
		if !r.PolicyInSession(ctx, "s", 0, ws) {
			return errors.New("semantic_authority_changed")
		}
		return nil
	})
	if e == nil || result.Created != 0 {
		t.Fatal("disabled write", result, e)
	}
}
func TestConcreteSemanticNotificationsAfterCommitNotCheckpoint(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			r, _, _, _ := semanticOwnerSetup(t)
			ctx := context.Background()
			err := r.Memory.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
				var record *Object
				if e := tx.Store.Read(ctx, func(reader *assertions.Reader) error { var e error; record, e = reader.Record("a"); return e }); e != nil {
					return e
				}
				record = record.Clone()
				_ = record.Set("content", "Changed synthetic declaration")
				if e := tx.Repository.Write(record); e != nil {
					return e
				}
				if e := tx.Checkpoint(); e != nil {
					return e
				}
				if r.Learning.Stats().Accepted != 0 {
					t.Fatal("checkpoint escaped before root outcome")
				}
				if rollback {
					return errors.New("rollback synthetic checkpoint")
				}
				return nil
			})
			if rollback != (err != nil) {
				t.Fatal(err)
			}
			want := 1
			if rollback {
				want = 0
			}
			if stats := r.Learning.Stats(); stats.Accepted != want {
				t.Fatal(stats)
			}
		})
	}
}
func TestSemanticShutdownJoinsPendingRootObserver(t *testing.T) {
	r, f, refs, _ := semanticOwnerSetup(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	releaseRoot := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRoot()
	go func() {
		finished <- f.recovery.WithWriteSession(context.Background(), f.main, func(ws *recovery.WriteSession) error {
			if e := r.NotifyInSession(ws, "s", refs[0], "assertion_committed"); e != nil {
				return e
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("root exited before observer registration: %v", err)
	case <-time.After(5 * time.Second):
		releaseRoot()
		t.Fatal("root did not reach observer registration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r.CloseAndWait(ctx) == nil {
		t.Fatal("unwound root reported drained")
	}
	releaseRoot()
	if e := <-finished; e != nil {
		t.Fatal(e)
	}
	if e := r.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if r.Learning.Stats().Accepted != 0 {
		t.Fatal("late callback enrolled after close")
	}
}

func TestSourceQualificationNotificationUsesExactRoot(t *testing.T) {
	r, f, _, _ := semanticOwnerSetup(t)
	// Match sourceevents/store_test.go ingress fixture: ingress is the command;
	// "new" is only the resulting mutation/status.
	change := sourceevents.Change{Kind: "ingress", Identity: sourceevents.Identity{SceneID: "s", AdapterID: "adapter", AccountID: "account", MessageID: "message-a", SenderID: "p"}, BodyHash: strings.Repeat("a", 64)}
	if !change.Valid() {
		t.Fatal("source ingress fixture violates authoritative Change validation")
	}
	result, e := f.events.Observe(context.Background(), change)
	if e != nil {
		t.Fatal(e)
	}
	if result.Status != "new" {
		t.Fatal(result)
	}
	if stats := r.Learning.Stats(); stats.Accepted != 1 {
		t.Fatal(stats)
	}
}
