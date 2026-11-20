package providertransport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Role deadlines are driven by the test; provider HTTP deadlines are real.
type sqlObserverRoleClock struct {
	roleMS float64
	roles  chan *sqlObserverRoleTimer
}
type sqlObserverRoleTimer struct {
	done     atomic.Bool
	callback func()
}

func (c sqlObserverRoleClock) Now() float64 { return float64(time.Now().UnixMilli()) }
func (c sqlObserverRoleClock) AfterFunc(ms float64, callback func()) modelservice.Timer {
	if ms != c.roleMS {
		return time.AfterFunc(time.Duration(ms*float64(time.Millisecond)), callback)
	}
	timer := &sqlObserverRoleTimer{callback: callback}
	c.roles <- timer
	return timer
}
func (t *sqlObserverRoleTimer) Stop() bool { return !t.done.Swap(true) }
func (t *sqlObserverRoleTimer) fire() {
	if t.Stop() {
		t.callback()
	}
}

func TestBorrowedScopeRoleTimeoutWaitsActualMetadataSQLAndKeepsModelsOpen(t *testing.T) {
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	raw, e := os.ReadFile("../modelcalls/testdata/source-output.json")
	if e != nil {
		t.Fatal(e)
	}
	var source struct{ Schema string }
	if e = json.Unmarshal(raw, &source); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", filepath.Join(t.TempDir(), "metadata.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(source.Schema); e != nil {
		t.Fatal(e)
	}
	held, e := db.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	m := diagnosticModels(t, func(w http.ResponseWriter, _ *http.Request) { diagnosticResponse(w) })
	retries, providerTimeout, roleTimeout := float64(0), float64(3000), float64(100)
	clock := sqlObserverRoleClock{roleMS: roleTimeout, roles: make(chan *sqlObserverRoleTimer, 2)}
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "openai-chat", RetryCount: &retries, TimeoutMS: &providerTimeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "fixture", Capabilities: []string{"text"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}, TimeoutMS: &roleTimeout}}}, modelservice.Options{Clock: clock})
	if e != nil {
		t.Fatal(e)
	}
	// diagnosticModels supplies the same p/m synthetic transport; replace only the
	// policy's role deadline, never the real SQL observation path.
	m.Policy = policy
	owner := modelcalls.New(db)
	entered := make(chan struct{})
	var once sync.Once
	m.Calls = callSinkFunc(func(ctx context.Context, v modelcalls.Observation) error {
		once.Do(func() { close(entered) })
		return owner.Record(ctx, v)
	})
	var group quiescence.Group
	root, _ := group.Start()
	returned := make(chan error, 1)
	go func() {
		value, e := m.Complete(WithPhysicalWork(ctx, &group), diagnosticRequest())
		if value != nil {
			value.Release()
		}
		returned <- e
	}()
	select {
	case <-entered:
	case e := <-returned:
		t.Fatalf("logical completion before SQL entered: %v", e)
	case <-ctx.Done():
		t.Fatal("SQL observer not entered", ctx.Err())
	}
	// The role timer starts before its provider can enter the SQL observer.
	// Drive only this first timer, while the connection is still checked out.
	select {
	case timer := <-clock.roles:
		timer.fire()
	case <-ctx.Done():
		t.Fatal("role timer not registered", ctx.Err())
	}
	select {
	case e := <-returned:
		if e == nil || e.Error() != "model_stage_timeout" {
			t.Fatalf("expected role timeout while SQL owner is held: %v", e)
		}
	case <-ctx.Done():
		t.Fatal("role timeout did not release logical caller", ctx.Err())
	}
	root()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if e = group.Wait(cancelled); !errors.Is(e, context.Canceled) {
		t.Fatalf("logical timeout lost actual SQL observer: %v", e)
	}
	var n int
	if e = held.QueryRowContext(ctx, "SELECT COUNT(*) FROM model_calls").Scan(&n); e != nil || n != 0 {
		t.Fatalf("unexpected metadata publication %d %v", n, e)
	}
	if e = held.Close(); e != nil {
		t.Fatal(e)
	}
	if e = group.Wait(ctx); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM model_calls").Scan(&n); e != nil || n != 1 {
		t.Fatalf("lost settled metadata %d %v", n, e)
	}
	value, e := m.Complete(ctx, diagnosticRequest())
	if e != nil {
		t.Fatal("caller scope closed shared Models", e)
	}
	value.Release()
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM model_calls").Scan(&n); e != nil || n != 2 {
		t.Fatalf("next caller metadata %d %v", n, e)
	}
}
