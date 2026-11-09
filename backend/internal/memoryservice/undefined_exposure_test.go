package memoryservice

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestModelMemoryExposureSharedUndefinedActualSourceOracle(t *testing.T) {
	raw, err := os.ReadFile("../jsjson/testdata/native-undefined.json")
	exposureMust(t, err)
	var fixture struct {
		Record   json.RawMessage
		Outcomes []map[string]any
	}
	exposureMust(t, json.Unmarshal(raw, &fixture))
	for _, want := range fixture.Outcomes {
		mode := want["mode"].(string)
		t.Run(mode, func(t *testing.T) {
			s, f := serviceSetup(t)
			ctx := context.Background()
			c := exposureLiveTurn(t, s, f)
			port, err := NewExposurePort(s, f.events, f.main, f.recovery)
			exposureMust(t, err)
			tracker, err := NewWorkspaceSelectionTracker(s, func() bool { return true }, nil)
			exposureMust(t, err)
			exposure, err := NewModelMemoryExposure(port, tracker.Capture)
			exposureMust(t, err)
			v, err := jsjson.Parse(string(fixture.Record))
			exposureMust(t, err)
			record := v.(*Object)
			exposureWrite(t, f, "INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,?,?,?,?,?)", "shared", "shared:public", nil, "Synthetic", string(fixture.Record), "shared")
			fresh := func() *botruntime.TurnContext {
				return &botruntime.TurnContext{Scene: c.Scene, Mode: mode, TurnID: "synthetic-turn"}
			}
			current, err := s.List(ctx, Context{SceneID: "s", Simulation: mode == "simulation"}, "")
			exposureMust(t, err)
			if len(current) != 1 {
				t.Fatal("shared list", len(current))
			}
			projected := current[0]
			supersedes, own := projected.Get("supersedesId")
			var before, after int
			exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&before))
			rawN := len(exposurePrepare(t, exposure, fresh(), record))
			missing := exposureClone(projected).(*Object)
			missing.Delete("supersedesId")
			deleted := len(exposurePrepare(t, exposure, fresh(), missing))
			null := exposureClone(projected).(*Object)
			_ = null.Set("supersedesId", nil)
			nulled := len(exposurePrepare(t, exposure, fresh(), null))
			deliveredContext := fresh()
			prepared := exposurePrepare(t, exposure, deliveredContext, projected)
			delivered := exposureCommit(t, exposure, deliveredContext, selectedMemory(prepared))
			exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&after))
			originalJSON, err := jsjson.Stringify(record)
			exposureMust(t, err)
			projectedJSON, err := jsjson.Stringify(projected)
			exposureMust(t, err)
			var writes any
			if mode == "simulation" {
				writes = after - before
			}
			got := map[string]any{"mode": mode, "raw": rawN, "deleted": deleted, "nulled": nulled, "prepared": len(prepared), "delivered": delivered, "ownUndefined": own && jsjson.IsUndefined(supersedes), "jsonSame": projectedJSON == originalJSON, "exposed": exposure.ReadExposed(deliveredContext), "simulationWrites": writes}
			encoded, err := json.Marshal(got)
			exposureMust(t, err)
			var normalized any
			exposureMust(t, json.Unmarshal(encoded, &normalized))
			if !reflect.DeepEqual(normalized, want) {
				t.Fatalf("actual source mismatch\ngot %s\nwant %#v", encoded, want)
			}
		})
	}
}

// These helpers force stack roots out of scope before collection. They exercise
// real Prepare/Commit, not a manufactured cleanup registration or a fake port.
//
//go:noinline
func preparedStateWithHeldReceipt(t *testing.T, e *ModelMemoryExposure, record *Object) (*Object, weak.Pointer[memoryExposureState]) {
	c := &botruntime.TurnContext{Scene: botruntime.Scene{ID: "s"}, Mode: "real", TurnID: "gc-held"}
	p := exposurePrepare(t, e, c, record)
	if len(p) != 1 {
		t.Fatal("prepare")
	}
	if exposureCommit(t, e, c, selectedMemory(p)) != 1 {
		t.Fatal("commit")
	}
	return p[0], weak.Make(e.turn(c))
}

//go:noinline
func preparedStateWithReleasedReceipt(t *testing.T, e *ModelMemoryExposure, record *Object) *memoryExposureState {
	c := &botruntime.TurnContext{Scene: botruntime.Scene{ID: "s"}, Mode: "real", TurnID: "gc-release"}
	p := exposurePrepare(t, e, c, record)
	if len(p) != 1 {
		t.Fatal("prepare")
	}
	state := e.turn(c)
	runtime.KeepAlive(p)
	return state
}

//go:noinline
func exposureStateGone(p weak.Pointer[memoryExposureState]) bool { return p.Value() == nil }
func TestModelMemoryExposureWeakCleanupLifetime(t *testing.T) {
	s, f := serviceSetup(t)
	port, err := NewExposurePort(s, f.events, f.main, f.recovery)
	exposureMust(t, err)
	tracker, err := NewWorkspaceSelectionTracker(s, func() bool { return true }, nil)
	exposureMust(t, err)
	e, err := NewModelMemoryExposure(port, tracker.Capture)
	exposureMust(t, err)
	record := putRecord(t, f, "a", `,"importance":"low","confidence":0.5,"locked":false,"origin":"manual","lineageStatus":"unverified"`)
	receipt, stateWeak := preparedStateWithHeldReceipt(t, e, record)
	deadline := time.Now().Add(4 * time.Second)
	for !exposureStateGone(stateWeak) && time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if !exposureStateGone(stateWeak) {
		t.Fatal("retained receipt kept old turn snapshots alive")
	}
	if text(receipt, "id") != "a" {
		t.Fatal("held receipt corrupted")
	}
	runtime.KeepAlive(receipt)
	state := preparedStateWithReleasedReceipt(t, e, record)
	pending := func() int { state.mu.Lock(); defer state.mu.Unlock(); return len(state.pending) }
	deadline = time.Now().Add(4 * time.Second)
	for pending() != 0 && time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if n := pending(); n != 0 {
		t.Fatalf("dead receipt retained %d pending snapshots", n)
	}
	runtime.KeepAlive(state)
}
