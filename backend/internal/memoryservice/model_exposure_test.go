package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"os"
	"reflect"
	"testing"
	"time"
)

func exposureMust(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}

// Obtain a genuine turn, with actual source authority, while its extension is
// held before any model call. All statements use the same one-connection DB.
func exposureLiveTurn(t *testing.T, s *Service, f scopedFixture) *botruntime.TurnContext {
	t.Helper()
	ctx := context.Background()
	_, err := f.main.Exec(`CREATE TABLE IF NOT EXISTS config(key TEXT PRIMARY KEY,value TEXT NOT NULL); UPDATE scenes SET value='{"id":"s","kind":"group","adapterId":"adapter","accountId":"account","platformSceneId":"s","participationEnabled":true,"learningEnabled":false,"proactiveEnabled":false}' WHERE id='s'`)
	exposureMust(t, err)
	authority, err := sourceevents.NewRuntimeAuthority(f.events)
	exposureMust(t, err)
	policy, err := replyplan.New(s.d.Authority.Compare, s.d.Authority.DateParser)
	exposureMust(t, err)
	settings := botruntime.DefaultSettings()
	settings.TopicAdmissionEnabled = false
	settings.PromptContextWindowEnabled = false
	runtime, err := botruntime.New(settings, botruntime.Services{Authority: authority, Dates: s.d.Authority.DateParser, Policy: policy, Compare: s.d.Authority.Compare, Models: botruntime.ModelFunc(func(_ context.Context, _ botruntime.ModelRequest, settle func(botruntime.Completion, error)) {
		settle(botruntime.Completion{}, errors.New("unexpected_model"))
	})})
	exposureMust(t, err)
	arrived := make(chan *botruntime.TurnContext, 1)
	release := make(chan struct{})
	runtime.AddExtension(&botruntime.Extension{ContextPrompt: func(c *botruntime.TurnContext) (string, error) {
		arrived <- c
		<-release
		return "", errors.New("synthetic_turn_complete")
	}})
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s", Kind: "group", AdapterID: "adapter", AccountID: "account", PlatformSceneID: "s"}, ParticipationEnabled: true}
	exposureMust(t, runtime.RegisterScene(scene))
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	message := epObj("id", "turn-source", "sceneId", "s", "senderId", "p", "origin", "live", "status", "available", "sentAt", stamp, "receivedAt", stamp, "parts", []any{epObj("type", "text", "text", "Synthetic input")})
	_, err = runtime.Ingest(ctx, "s", message, false)
	exposureMust(t, err)
	snapshot, err := runtime.Snapshot("s")
	exposureMust(t, err)
	if len(snapshot.Topics) == 0 {
		t.Fatal("topic absent")
	}
	handle, err := runtime.Run("s", snapshot.Topics[0].ID, false)
	exposureMust(t, err)
	t.Cleanup(func() {
		runtime.Cancel("s")
		close(release)
		wait, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		exposureMust(t, runtime.CloseAndWait(wait))
	})
	select {
	case c := <-arrived:
		return c
	case <-handle.Done:
		t.Fatalf("turn ended before memory boundary: %+v", handle.Wait())
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not enter memory boundary")
	}
	return nil
}
func selectedMemory(values []*Object) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
func exposureCommit(t *testing.T, e *ModelMemoryExposure, c *botruntime.TurnContext, values []any) int {
	t.Helper()
	out, err := e.Commit(context.Background(), c, values)
	exposureMust(t, err)
	return len(out)
}
func exposurePrepare(t *testing.T, e *ModelMemoryExposure, c *botruntime.TurnContext, values ...*Object) []*Object {
	t.Helper()
	out, err := e.Prepare(context.Background(), c, values)
	exposureMust(t, err)
	return out
}
func exposureCheck(t *testing.T, f scopedFixture, guard *botruntime.ToolResultGuard) bool {
	t.Helper()
	if guard == nil {
		t.Fatal("missing guard")
	}
	var ok bool
	exposureMust(t, f.events.WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error { var err error; ok, err = guard.Check(reads); return err }))
	return ok
}
func exposureValid(t *testing.T, f scopedFixture, c *botruntime.TurnContext) bool {
	t.Helper()
	var valid bool
	exposureMust(t, f.events.WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error {
		var err error
		valid, err = c.ValidateMemoryContextWith(reads)
		return err
	}))
	return valid
}
func exposureWrite(t *testing.T, f scopedFixture, query string, args ...any) {
	t.Helper()
	exposureMust(t, f.recovery.WithWriteSession(context.Background(), f.main, func(ws *recovery.WriteSession) error { _, err := ws.Exec(query, args...); return err }))
}
func TestModelMemoryExposureActualSourceOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/model-exposure-source.json")
	exposureMust(t, err)
	var fixture struct {
		Outcomes []struct {
			Name  string `json:"name"`
			Value any    `json:"value"`
		} `json:"outcomes"`
	}
	exposureMust(t, json.Unmarshal(raw, &fixture))
	for _, test := range fixture.Outcomes {
		t.Run(test.Name, func(t *testing.T) {
			s, f := serviceSetup(t)
			ctx := context.Background()
			c := exposureLiveTurn(t, s, f)
			port, err := NewExposurePort(s, f.events, f.main, f.recovery)
			exposureMust(t, err)
			tracker, err := NewWorkspaceSelectionTracker(s, func() bool { return true }, nil)
			exposureMust(t, err)
			delivered := 0
			var callback func() error
			exposure, err := NewModelMemoryExposure(port, func(ctx context.Context, c *botruntime.TurnContext, refs []Reference) error {
				delivered++
				if err := tracker.Capture(ctx, c, refs); err != nil {
					return err
				}
				if callback != nil {
					return callback()
				}
				return nil
			})
			exposureMust(t, err)
			put := func(id string) *Object {
				return putRecord(t, f, id, `,"importance":"low","confidence":0.5,"locked":false,"origin":"manual","lineageStatus":"unverified"`)
			}
			var got any
			switch test.Name {
			case "identity_batch_and_exact_revision":
				p := exposurePrepare(t, exposure, c, put("a"), put("b"))
				before := exposure.ReadExposed(c)
				forged := []any{exposureClone(p[0]), exposureClone(p[1])}
				forgedN := exposureCommit(t, exposure, c, forged)
				mixed := exposureCommit(t, exposure, c, []any{p[0], exposureClone(p[1])})
				foreign := exposureCommit(t, exposure, &botruntime.TurnContext{Scene: c.Scene, Mode: "real"}, selectedMemory(p))
				selected := exposureCommit(t, exposure, c, []any{p[0]})
				guard := exposure.CaptureExactGuard(c, "a")
				valid := exposureCheck(t, f, guard)
				exposureWrite(t, f, "UPDATE memory_assertion_state SET version=version+1 WHERE assertion_id=?", "a")
				got = map[string]any{"prepared": len(p), "before": before, "forged": forgedN, "mixed": mixed, "foreign": foreign, "selected": selected, "exposed": exposure.ReadExposed(c), "valid": valid, "revoked": exposureCheck(t, f, guard), "delivered": delivered}
			case "stale_sibling_is_atomic":
				p := exposurePrepare(t, exposure, c, put("a"), put("b"))
				exposureWrite(t, f, "UPDATE memory_assertion_state SET deletion_epoch=deletion_epoch+1 WHERE assertion_id=?", "b")
				n := exposureCommit(t, exposure, c, selectedMemory(p))
				got = map[string]any{"selected": n, "exposed": exposure.ReadExposed(c), "delivered": delivered}
			case "callback_mutation_reauthorizes":
				p := exposurePrepare(t, exposure, c, put("a"))
				callback = func() error { _, err := s.Lock(ctx, "a", true); return err }
				n := exposureCommit(t, exposure, c, selectedMemory(p))
				got = map[string]any{"selected": n, "exposed": exposure.ReadExposed(c), "valid": exposureValid(t, f, c), "delivered": delivered}
			case "callback_cannot_replace_selected_identity":
				p := exposurePrepare(t, exposure, c, put("a"))
				original := p[0]
				selected := selectedMemory(p)
				callback = func() error {
					forged := original.Clone()
					_ = forged.Set("content", "forged during callback")
					selected[0] = forged
					return nil
				}
				out, err := exposure.Commit(ctx, c, selected)
				exposureMust(t, err)
				if len(out) != 1 {
					t.Fatal("receipt lost")
				}
				got = map[string]any{"selected": len(out), "original": out[0] == original, "content": value(out[0], "content"), "exposed": exposure.ReadExposed(c), "delivered": delivered}
			case "callback_failure_invalidates":
				p := exposurePrepare(t, exposure, c, put("a"))
				callback = func() error { return errors.New("synthetic_callback") }
				_, err := exposure.Commit(ctx, c, selectedMemory(p))
				if err == nil {
					t.Fatal("callback error lost")
				}
				got = map[string]any{"error": err.Error(), "exposed": exposure.ReadExposed(c), "valid": exposureValid(t, f, c), "delivered": delivered}
			case "first_exposure_is_immutable":
				p := exposurePrepare(t, exposure, c, put("a"))
				exposureCommit(t, exposure, c, selectedMemory(p))
				_, err := s.Lock(ctx, "a", true)
				exposureMust(t, err)
				records, err := s.List(ctx, Context{SceneID: "s"}, "")
				exposureMust(t, err)
				next := exposurePrepare(t, exposure, c, records...)
				n := exposureCommit(t, exposure, c, selectedMemory(next))
				got = map[string]any{"selected": n, "exposed": exposure.ReadExposed(c), "valid": exposureValid(t, f, c), "delivered": delivered}
			case "duplicate_scores_and_conflicting_bodies":
				a := put("a")
				other := a.Clone()
				_ = other.Set("content", "different")
				conflict := len(exposurePrepare(t, exposure, c, a, other))
				one, two := a.Clone(), a.Clone()
				_ = one.Set("score", float64(1))
				_ = two.Set("score", float64(2))
				p := exposurePrepare(t, exposure, c, one, two)
				n := exposureCommit(t, exposure, c, selectedMemory(p))
				got = map[string]any{"conflict": conflict, "prepared": len(p), "selected": n, "exposed": exposure.ReadExposed(c), "delivered": delivered}
			case "simulation_does_not_capture_real_refs":
				// The receipt uses the source simulation context over shared public data.
				simulated := &botruntime.TurnContext{Scene: c.Scene, Mode: "simulation", TurnID: "simulation"}
				a := put("a")
				_ = a.Set("scope", epObj("kind", "sharedPublicKnowledge"))
				raw, err := jsjson.Stringify(a)
				exposureMust(t, err)
				exposureWrite(t, f, "UPDATE memories SET scope_key='shared:public',value=? WHERE id='a'", raw)
				var before, after int
				exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&before))
				p := exposurePrepare(t, exposure, simulated, a)
				n := exposureCommit(t, exposure, simulated, selectedMemory(p))
				exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&after))
				got = map[string]any{"prepared": len(p), "selected": n, "exposed": exposure.ReadExposed(simulated), "writes": after - before, "delivered": delivered}
			case "workspace_receipts_use_retained_bodies":
				records := []*Object{}
				for i := 0; i < 81; i++ {
					records = append(records, put(fmt.Sprint("a", i)))
				}
				p := exposurePrepare(t, exposure, c, records...)
				exposureCommit(t, exposure, c, selectedMemory(p))
				brief := parseObject(t, `{"evidence":[{"memories":[{"id":"a0"},{"id":"never-delivered"}],"states":[{"page":{"items":[{"kind":"assertion","snapshot":{"ref":{"id":"a40"}}},{"kind":"reported_experience","assertionRef":{"id":"a80"}},{"kind":"commitment","assertionRef":{"id":"a20"}}]}}]}]}`)
				exposureMust(t, tracker.Selected(ctx, c, brief))
				exposureMust(t, tracker.Selected(ctx, c, brief))
				rows, err := f.main.Query("SELECT json_extract(value,'$.snapshot.ref.id'),json_extract(value,'$.contextSelectedCount') FROM memory_working_set_items WHERE json_extract(value,'$.contextSelectedCount')>0 ORDER BY 1")
				exposureMust(t, err)
				values := []any{}
				for rows.Next() {
					var id string
					var count int
					exposureMust(t, rows.Scan(&id, &count))
					values = append(values, map[string]any{"id": id, "count": count})
				}
				exposureMust(t, rows.Err())
				exposureMust(t, rows.Close())
				allMemories := make([]any, len(records))
				for i, record := range records {
					allMemories[i] = epObj("id", text(record, "id"))
				}
				allBrief := epObj("evidence", []any{epObj("memories", allMemories)})
				exposureMust(t, tracker.Selected(ctx, c, allBrief))
				exposureMust(t, tracker.Selected(ctx, c, allBrief))
				var allCount, total int
				exposureMust(t, f.main.QueryRow("SELECT count(*),sum(json_extract(value,'$.contextSelectedCount')) FROM memory_working_set_items WHERE json_extract(value,'$.contextSelectedCount')>0").Scan(&allCount, &total))
				got = map[string]any{"prepared": len(p), "exposed": len(exposure.ReadExposed(c)), "rows": values, "allRows": map[string]any{"n": allCount, "total": total}}
			default:
				t.Fatal("unimplemented actual source case", test.Name)
			}
			encoded, err := json.Marshal(got)
			exposureMust(t, err)
			var normalized any
			exposureMust(t, json.Unmarshal(encoded, &normalized))
			if !reflect.DeepEqual(normalized, test.Value) {
				want, _ := json.Marshal(test.Value)
				t.Fatalf("source mismatch\ngot %s\nwant%s", encoded, want)
			}
		})
	}
}

func TestModelMemoryExposureScopedGuardWriterRollbackCancellationAndTamper(t *testing.T) {
	s, f := serviceSetup(t)
	ctx := context.Background()
	c := exposureLiveTurn(t, s, f)
	port, err := NewExposurePort(s, f.events, f.main, f.recovery)
	exposureMust(t, err)
	tracker, err := NewWorkspaceSelectionTracker(s, func() bool { return true }, nil)
	exposureMust(t, err)
	exposure, err := NewModelMemoryExposure(port, tracker.Capture)
	exposureMust(t, err)
	record := putRecord(t, f, "a", "")
	_ = record.Set("sources", []any{epObj("sceneId", "s", "messageId", "memory-source", "senderId", "p")})
	raw, err := jsjson.Stringify(record)
	exposureMust(t, err)
	exposureWrite(t, f, "UPDATE memories SET value=? WHERE id='a'", raw)
	p := exposurePrepare(t, exposure, c, record)
	if len(p) != 1 {
		t.Fatal("prepare denied actual record")
	}
	_ = p[0].Set("content", "tampered")
	if n := exposureCommit(t, exposure, c, selectedMemory(p)); n != 0 {
		t.Fatal("mutated identity accepted")
	}
	if len(exposure.ReadExposed(c)) != 0 {
		t.Fatal("tampered receipt partially exposed")
	}
	p = exposurePrepare(t, exposure, c, record)
	if n := exposureCommit(t, exposure, c, selectedMemory(p)); n != 1 {
		t.Fatal("delivery failed")
	}
	found := false
	for _, id := range c.Dependencies() {
		found = found || id == "memory-source"
	}
	if !found {
		t.Fatal("delivered source dependency missing")
	}
	guard := exposure.CaptureExactGuard(c, "a")
	if !exposureCheck(t, f, guard) {
		t.Fatal("fresh guard invalid")
	}
	rollback := errors.New("synthetic_rollback")
	err = f.recovery.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		if _, err := ws.Exec("UPDATE memory_assertion_state SET version=version+1 WHERE assertion_id='a'"); err != nil {
			return err
		}
		if err := f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			ok, err := guard.Check(reads)
			if err != nil {
				return err
			}
			if ok {
				t.Fatal("guard ignored same-child uncommitted revision")
			}
			valid, err := c.ValidateMemoryContextWith(reads)
			if err != nil {
				return err
			}
			if valid {
				t.Fatal("turn validation ignored writer revision")
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
		t.Fatal("rollback did not restore original exact reference")
	}
	other, foreign := serviceSetup(t)
	_ = other
	err = foreign.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		ok, err := guard.Check(reads)
		if ok || err != nil {
			t.Fatal("foreign owner did not fail closed", err)
		}
		if _, err := port.SnapshotScoped(reads, Context{SceneID: "s"}, []string{"a"}); err == nil {
			t.Fatal("port accepted foreign owner")
		}
		return nil
	})
	exposureMust(t, err)
	c.Signal().Abort(errors.New("old_turn_finished"))
	if !exposureCheck(t, f, guard) {
		t.Fatal("detached guard inherited old turn cancellation")
	}
	if n := exposureCommit(t, exposure, c, selectedMemory(p)); n != 0 {
		t.Fatal("aborted turn committed")
	}
	exposureWrite(t, f, "INSERT INTO recall_tombstones VALUES('s','memory-source','2026-11-08')")
	if exposureCheck(t, f, guard) {
		t.Fatal("recall revived cached exact body")
	}
}
