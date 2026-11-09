package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Pair two genuine identity queries to exercise shared StateExposure's whole-
// group rules. Validity delegates to those concrete current/scoped read owners.
type pairedIdentityPort struct {
	base    *identityExposurePort
	persons []string
}

func (p pairedIdentityPort) Query(ctx context.Context, c Context, _ any) (out []*Object, err error) {
	for _, person := range p.persons {
		v, e := p.base.Query(ctx, c, person)
		if e != nil {
			return nil, e
		}
		out = append(out, v...)
	}
	return
}
func (p pairedIdentityPort) Valid(ctx context.Context, c Context, values []*Object) (bool, error) {
	current, err := p.Query(ctx, c, nil)
	return err == nil && sameStateValues(current, values), nil
}
func (p pairedIdentityPort) ValidScoped(reads botruntime.ReadAuthority, c Context, values []*Object) (bool, error) {
	current := []*Object{}
	for _, person := range p.persons {
		v, err := p.base.current(reads, c, person)
		if err != nil {
			return false, nil
		}
		current = append(current, v...)
	}
	return sameStateValues(current, values), nil
}
func (p pairedIdentityPort) SourceIDs(v []*Object) ([]string, error) { return p.base.SourceIDs(v) }
func identityViewsJSON(t *testing.T, values []*Object) json.RawMessage {
	t.Helper()
	v := make([]any, len(values))
	for i, o := range values {
		v[i] = o
	}
	raw, err := jsjson.Marshal(v)
	exposureMust(t, err)
	return raw
}
func TestIdentityExposureActualSourceOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/identity-exposure-source.json")
	exposureMust(t, err)
	var fixture struct {
		Records  []json.RawMessage
		Outcomes []struct {
			Name     string
			Registry json.RawMessage
			Value    map[string]any
		}
	}
	exposureMust(t, json.Unmarshal(raw, &fixture))
	for _, test := range fixture.Outcomes {
		t.Run(test.Name, func(t *testing.T) {
			s, f := serviceSetup(t)
			ctx := context.Background()
			c := exposureLiveTurn(t, s, f)
			_, err := f.main.Exec("CREATE TABLE memory_identity_registry(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)")
			exposureMust(t, err)
			exposureWrite(t, f, "INSERT INTO memory_identity_registry VALUES(1,?)", string(test.Registry))
			for _, raw := range fixture.Records {
				v, err := jsjson.Parse(string(raw))
				exposureMust(t, err)
				record := v.(*Object)
				exposureWrite(t, f, "INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,'scene:s',?,?,?,?)", text(record, "id"), text(object(value(record, "scope")), "personId"), "Synthetic", string(raw), text(record, "id"))
			}
			port, err := NewExposurePort(s, f.events, f.main, f.recovery)
			exposureMust(t, err)
			policy, err := modelread.NewRuntime(s.d.Assertions, f.events, f.main, f.recovery)
			exposureMust(t, err)
			identity, err := NewIdentityExposure(port, policy)
			exposureMust(t, err)
			query := func(c *botruntime.TurnContext, person string) []*Object {
				v, err := identity.Query(ctx, c, person)
				exposureMust(t, err)
				return v
			}
			deliver := func(c *botruntime.TurnContext, values []any) int {
				v, err := identity.Deliver(ctx, c, values)
				exposureMust(t, err)
				return len(v)
			}
			var got any
			switch test.Name {
			case "identity_exact_scope_and_receipts":
				values := query(c, "p")
				noneBefore := identity.CaptureGuard(c) == nil
				forged := make([]any, len(values))
				for i, v := range values {
					forged[i] = exposureClone(v)
				}
				forgedN := deliver(c, forged)
				foreign := deliver(&botruntime.TurnContext{Scene: c.Scene, Mode: "real"}, selectedMemory(values))
				selected := deliver(c, selectedMemory(values))
				guard := identity.CaptureGuard(c)
				got = map[string]any{"values": identityViewsJSON(t, values), "noneBefore": noneBefore, "forged": forgedN, "foreign": foreign, "delivered": selected, "repeat": deliver(c, selectedMemory(values)), "valid": exposureCheck(t, f, guard), "dependencies": withoutTurnSource(c.Dependencies()), "turnValid": exposureValid(t, f, c), "simulation": len(query(&botruntime.TurnContext{Scene: c.Scene, Mode: "simulation"}, "p")), "invisible": len(query(c, "invisible"))}
			case "identity_recall_and_detached_guard":
				values := query(c, "p")
				deliver(c, selectedMemory(values))
				guard := identity.CaptureGuard(c)
				exposureWrite(t, f, "INSERT INTO recall_tombstones VALUES(?,?,?)", "s", "observed-name", "2026-10-28T12:00:00.000Z")
				got = map[string]any{"guard": exposureCheck(t, f, guard), "stillPresent": identity.CaptureGuard(c) != nil, "turnValid": exposureValid(t, f, c)}
			case "identity_policy_revocation":
				values := query(c, "p")
				deliver(c, selectedMemory(values))
				guard := identity.CaptureGuard(c)
				exposureWrite(t, f, "UPDATE scenes SET value=json_set(value,'$.personalizationEnabled',json('false')) WHERE id='s'")
				got = map[string]any{"guard": exposureCheck(t, f, guard), "stillPresent": identity.CaptureGuard(c) != nil, "query": len(query(c, "p"))}
			case "state_whole_order_callback_once":
				calls := 0
				states, err := NewStateExposure(pairedIdentityPort{identity.port, []string{"p", "q"}}, func(context.Context, *botruntime.TurnContext, []*Object) error { calls++; return nil })
				exposureMust(t, err)
				values, err := states.Query(ctx, c, nil)
				exposureMust(t, err)
				deliver := func(v []any) int { out, err := states.Deliver(ctx, c, v); exposureMust(t, err); return len(out) }
				partial := deliver(selectedMemory(values[:1]))
				reordered := deliver([]any{values[1], values[0]})
				selected := deliver(selectedMemory(values))
				repeat := deliver(selectedMemory(values))
				read, err := states.ReadDelivered(ctx, c)
				exposureMust(t, err)
				got = map[string]any{"partial": partial, "reordered": reordered, "selected": selected, "repeat": repeat, "delivered": calls, "read": len(read), "dependencies": withoutTurnSource(c.Dependencies()), "turnValid": exposureValid(t, f, c)}
			case "state_callback_failure_sticky":
				states, err := NewStateExposure(pairedIdentityPort{identity.port, []string{"p"}}, func(context.Context, *botruntime.TurnContext, []*Object) error {
					return errors.New("synthetic_callback")
				})
				exposureMust(t, err)
				values, err := states.Query(ctx, c, nil)
				exposureMust(t, err)
				_, err = states.Deliver(ctx, c, selectedMemory(values))
				if err == nil {
					t.Fatal("callback error lost")
				}
				message := err.Error()
				read, err := states.ReadDelivered(ctx, c)
				exposureMust(t, err)
				got = map[string]any{"error": message, "read": len(read), "hasGuard": states.CaptureGuard(c) != nil, "turnValid": exposureValid(t, f, c)}
			case "state_callback_reauthorization":
				calls := 0
				after, err := json.Marshal(test.Value["registryAfter"])
				exposureMust(t, err)
				states, err := NewStateExposure(pairedIdentityPort{identity.port, []string{"p"}}, func(context.Context, *botruntime.TurnContext, []*Object) error {
					calls++
					return f.recovery.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
						_, err := ws.Exec("UPDATE memory_identity_registry SET value=? WHERE id=1", string(after))
						return err
					})
				})
				exposureMust(t, err)
				values, err := states.Query(ctx, c, nil)
				exposureMust(t, err)
				selected, err := states.Deliver(ctx, c, selectedMemory(values))
				exposureMust(t, err)
				read, err := states.ReadDelivered(ctx, c)
				exposureMust(t, err)
				got = map[string]any{"selected": len(selected), "delivered": calls, "read": len(read), "hasGuard": states.CaptureGuard(c) != nil, "turnValid": exposureValid(t, f, c), "registryAfter": json.RawMessage(after)}
			default:
				t.Fatal(test.Name)
			}
			encoded, err := json.Marshal(got)
			exposureMust(t, err)
			var normalized any
			exposureMust(t, json.Unmarshal(encoded, &normalized))
			if !reflect.DeepEqual(normalized, test.Value) {
				t.Fatalf("source mismatch\ngot %s\nwant %#v", encoded, test.Value)
			}
		})
	}
}
func withoutTurnSource(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		if id != "turn-source" {
			out = append(out, id)
		}
	}
	return out
}

func TestIdentityExposureScopedWriterRollbackAndCancellation(t *testing.T) {
	raw, err := os.ReadFile("testdata/identity-exposure-source.json")
	exposureMust(t, err)
	var fixture struct {
		Records  []json.RawMessage
		Outcomes []struct{ Registry json.RawMessage }
	}
	exposureMust(t, json.Unmarshal(raw, &fixture))
	s, f := serviceSetup(t)
	ctx := context.Background()
	c := exposureLiveTurn(t, s, f)
	_, err = f.main.Exec("CREATE TABLE memory_identity_registry(id INTEGER PRIMARY KEY,value TEXT NOT NULL)")
	exposureMust(t, err)
	exposureWrite(t, f, "INSERT INTO memory_identity_registry VALUES(1,?)", string(fixture.Outcomes[0].Registry))
	for _, raw := range fixture.Records {
		v, err := jsjson.Parse(string(raw))
		exposureMust(t, err)
		r := v.(*Object)
		exposureWrite(t, f, "INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,'scene:s',?,'Synthetic',?,?)", text(r, "id"), text(object(value(r, "scope")), "personId"), string(raw), text(r, "id"))
	}
	port, err := NewExposurePort(s, f.events, f.main, f.recovery)
	exposureMust(t, err)
	policy, err := modelread.NewRuntime(s.d.Assertions, f.events, f.main, f.recovery)
	exposureMust(t, err)
	identity, err := NewIdentityExposure(port, policy)
	exposureMust(t, err)
	values, err := identity.Query(ctx, c, "p")
	exposureMust(t, err)
	out, err := identity.Deliver(ctx, c, selectedMemory(values))
	exposureMust(t, err)
	if len(out) != 1 {
		t.Fatal("delivery")
	}
	guard := identity.CaptureGuard(c)
	held, oldState := identityPendingHeld(t, identity)
	deadline := time.Now().Add(4 * time.Second)
	for !identityStateGone(oldState) && time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if !identityStateGone(oldState) {
		t.Fatal("held identity receipt retained old turn")
	}
	runtime.KeepAlive(held)
	pending := identityPendingReleased(t, identity)
	pendingCount := func() int { pending.mu.Lock(); defer pending.mu.Unlock(); return len(pending.pending) }
	deadline = time.Now().Add(4 * time.Second)
	for pendingCount() != 0 && time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if pendingCount() != 0 {
		t.Fatal("dead identity receipt retained snapshots")
	}
	runtime.KeepAlive(pending)
	var escaped botruntime.ReadAuthority
	exposureMust(t, f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { escaped = reads; return nil }))
	if valid, err := guard.Check(escaped); valid || err != nil {
		t.Fatal("expired scope accepted", err)
	}
	rollback := errors.New("rollback")
	err = f.recovery.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		if _, err := ws.Exec("UPDATE scenes SET value=json_set(value,'$.personalizationEnabled',json('false')) WHERE id='s'"); err != nil {
			return err
		}
		if err := f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			ok, err := guard.Check(reads)
			if ok || err != nil {
				t.Fatal("same writer permission ignored", err)
			}
			return nil
		}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if !exposureCheck(t, f, guard) {
		t.Fatal("rollback failed")
	}
	c.Signal().Abort(errors.New("old turn ended"))
	if !exposureCheck(t, f, guard) {
		t.Fatal("detached guard inherited cancellation")
	}
	_, foreign := serviceSetup(t)
	exposureMust(t, foreign.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		ok, err := guard.Check(reads)
		if ok || err != nil {
			t.Fatal("foreign scope accepted", err)
		}
		return nil
	}))
}

//go:noinline
func identityPendingHeld(t *testing.T, e *IdentityExposure) (*Object, weak.Pointer[stateExposureTurn]) {
	c := &botruntime.TurnContext{Scene: botruntime.Scene{ID: "s"}, Mode: "real"}
	v, err := e.Query(context.Background(), c, "p")
	exposureMust(t, err)
	if len(v) != 1 {
		t.Fatal("identity query")
	}
	return v[0], weak.Make(e.states.turn(c))
}

//go:noinline
func identityPendingReleased(t *testing.T, e *IdentityExposure) *stateExposureTurn {
	c := &botruntime.TurnContext{Scene: botruntime.Scene{ID: "s"}, Mode: "real"}
	v, err := e.Query(context.Background(), c, "p")
	exposureMust(t, err)
	if len(v) != 1 {
		t.Fatal("identity query")
	}
	state := e.states.turn(c)
	runtime.KeepAlive(v)
	return state
}

//go:noinline
func identityStateGone(p weak.Pointer[stateExposureTurn]) bool { return p.Value() == nil }
