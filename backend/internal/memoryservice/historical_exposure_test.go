package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type historicalOracle struct {
	Fixed    string
	Outcomes []struct {
		Name           string
		Record, Proofs json.RawMessage
		Value          map[string]any
	}
}

func loadHistoricalOracle(t *testing.T) historicalOracle {
	t.Helper()
	raw, err := os.ReadFile("testdata/historical-exposure-source.json")
	exposureMust(t, err)
	var fixture historicalOracle
	exposureMust(t, json.Unmarshal(raw, &fixture))
	return fixture
}
func historicalFixture(t *testing.T, fixed string, record, proofs json.RawMessage) (*Service, scopedFixture, *botruntime.TurnContext, *HistoricalExposure) {
	t.Helper()
	s, f := serviceSetup(t)
	store, err := assertions.New(f.main, f.recovery, assertions.Options{Now: func() string { return fixed }})
	exposureMust(t, err)
	deps := s.d
	deps.Assertions = store
	s, err = New(deps)
	exposureMust(t, err)
	f.store = store
	c := exposureLiveTurn(t, s, f)
	exposureWrite(t, f, "INSERT OR REPLACE INTO config(key,value) VALUES('settings',?)", `{"memoryHistoricalReadEnabled":true}`)
	v, err := jsjson.Parse(string(record))
	exposureMust(t, err)
	r := v.(*Object)
	exposureWrite(t, f, "INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,'scene:s','p',?,?,?)", text(r, "id"), text(r, "content"), string(record), text(r, "id"))
	exposureWrite(t, f, "INSERT INTO memory_supports(memory_id,value) VALUES(?,?)", text(r, "id"), string(proofs))
	port, err := NewExposurePort(s, f.events, f.main, f.recovery)
	exposureMust(t, err)
	exposure, err := NewHistoricalExposure(port)
	exposureMust(t, err)
	return s, f, c, exposure
}
func historicalJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := jsjson.Marshal(v)
	exposureMust(t, err)
	return raw
}
func historicalValuesJSON(t *testing.T, values []*Object) json.RawMessage {
	return historicalJSON(t, selectedMemory(values))
}
func TestHistoricalExposureActualSourceOracle(t *testing.T) {
	fixture := loadHistoricalOracle(t)
	for _, test := range fixture.Outcomes {
		t.Run(test.Name, func(t *testing.T) {
			s, f, c, e := historicalFixture(t, fixture.Fixed, test.Record, test.Proofs)
			ctx := context.Background()
			scope := Context{SceneID: "s"}
			query := func(c *botruntime.TurnContext, input *Object) []*Object {
				values, err := e.Query(ctx, c, input)
				exposureMust(t, err)
				return values
			}
			deliver := func(c *botruntime.TurnContext, values []any) int {
				out, err := e.Deliver(ctx, c, values)
				exposureMust(t, err)
				return len(out)
			}
			var got any
			switch test.Name {
			case "legacy_history_receipts_and_undefined":
				var before assertions.State
				exposureMust(t, f.store.Read(ctx, func(r *assertions.Reader) error { var err error; before, err = r.State("history"); return err }))
				history, err := s.HistoryFor(ctx, scope, "history")
				exposureMust(t, err)
				values := query(c, epObj("memoryId", "history"))
				asOf, hasAsOf := values[0].Get("asOfRecorded")
				validAt, hasValid := values[0].Get("validAt")
				forged := make([]any, len(values))
				for i, v := range values {
					forged[i] = exposureClone(v)
				}
				forgedN := deliver(c, forged)
				foreign := deliver(&botruntime.TurnContext{Scene: c.Scene, Mode: "real"}, selectedMemory(values))
				selected := deliver(c, selectedMemory(values))
				guard := e.CaptureGuard(c)
				got = map[string]any{"before": before, "history": historicalJSON(t, history), "values": historicalValuesJSON(t, values), "ownAsOf": hasAsOf && jsjson.IsUndefined(asOf), "ownValidAt": hasValid && jsjson.IsUndefined(validAt), "forged": forgedN, "foreign": foreign, "selected": selected, "repeat": deliver(c, selectedMemory(values)), "valid": exposureCheck(t, f, guard), "dependencies": withoutTurnSource(c.Dependencies()), "simulation": len(query(&botruntime.TurnContext{Scene: c.Scene, Mode: "simulation"}, epObj("memoryId", "history")))}
			case "query_limits_time_and_exact_anchor":
				_, err := s.HistoryFor(ctx, scope, "history")
				exposureMust(t, err)
				_, err = s.Lock(ctx, "history", true)
				exposureMust(t, err)
				full := query(c, epObj("memoryId", "history", "limit", float64(1)))
				old := query(c, epObj("memoryId", "history", "asOfRecorded", "2000-01-01T00:00:00.000Z"))
				failures := []any{}
				for _, q := range []*Object{epObj("memoryId", "history", "limit", float64(0)), epObj("memoryId", "history", "limit", float64(21)), epObj("memoryId", "history", "asOfRecorded", nil), epObj("memoryId", "history", "validAt", "2026-10-28"), epObj("memoryId", "history", "anchor", epObj("id", "history", "version", float64(1), "deletionEpoch", float64(0)))} {
					_, err := e.Query(ctx, c, q)
					if err == nil {
						failures = append(failures, nil)
					} else {
						failures = append(failures, err.Error())
					}
				}
				got = map[string]any{"full": historicalValuesJSON(t, full), "old": historicalValuesJSON(t, old), "errors": failures}
			case "revision_and_config_revoke_detached_guard":
				values := query(c, epObj("memoryId", "history"))
				deliver(c, selectedMemory(values))
				guard := e.CaptureGuard(c)
				_, err := s.Lock(ctx, "history", true)
				exposureMust(t, err)
				revisionValid := exposureCheck(t, f, guard)
				stale := deliver(c, selectedMemory(values))
				exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryHistoricalReadEnabled',json('false')) WHERE key='settings'")
				got = map[string]any{"revisionValid": revisionValid, "stale": stale, "guard": exposureCheck(t, f, guard), "stillPresent": e.CaptureGuard(c) != nil, "disabledQuery": len(query(c, epObj("memoryId", "history"))), "turnValid": exposureValid(t, f, c)}
			case "recall_changes_retained_privacy":
				values := query(c, epObj("memoryId", "history"))
				deliver(c, selectedMemory(values))
				guard := e.CaptureGuard(c)
				exposureWrite(t, f, "INSERT INTO recall_tombstones VALUES(?,?,?)", "s", "proof-source", fixture.Fixed)
				history, err := s.HistoryFor(ctx, scope, "history")
				exposureMust(t, err)
				got = map[string]any{"guard": exposureCheck(t, f, guard), "stillPresent": e.CaptureGuard(c) != nil, "history": historicalJSON(t, history), "next": historicalValuesJSON(t, query(c, epObj("memoryId", "history"))), "turnValid": exposureValid(t, f, c)}
			case "missing_source_metadata_qualifies_history":
				got = map[string]any{"values": historicalValuesJSON(t, query(c, epObj("memoryId", "history")))}
			case "bounded_history_and_result_windows":
				record, err := jsjson.Parse(string(test.Record))
				exposureMust(t, err)
				proofs, err := jsjson.Parse(string(test.Proofs))
				exposureMust(t, err)
				exposureMust(t, f.store.Transaction(ctx, func(tx *assertions.Store) error {
					for i := 0; i < 205; i++ {
						if _, err := tx.Append(epObj("record", record, "supports", proofs, "expectedVersion", float64(i), "expectedDeletionEpoch", float64(0), "idempotencyKey", fmt.Sprint("bulk-", i), "recordedAt", fixture.Fixed, "validFrom", nil, "validTo", nil, "eventKind", "admin_assertion")); err != nil {
							return err
						}
					}
					return nil
				}))
				history, err := s.HistoryFor(ctx, scope, "history")
				exposureMust(t, err)
				values := query(c, epObj("memoryId", "history"))
				revs := value(history, "revisions").([]any)
				views := value(values[0], "revisions").([]any)
				got = map[string]any{"historyCount": len(revs), "first": value(object(revs[0]), "version"), "last": value(object(revs[len(revs)-1]), "version"), "historyCoverage": value(history, "coverage"), "viewCount": len(views), "viewFirst": value(object(views[0]), "version"), "viewLast": value(object(views[len(views)-1]), "version"), "coverage": historicalJSON(t, value(values[0], "coverage"))}
			default:
				t.Fatal(test.Name)
			}
			raw, err := json.Marshal(got)
			exposureMust(t, err)
			var normalized any
			exposureMust(t, json.Unmarshal(raw, &normalized))
			if !reflect.DeepEqual(normalized, test.Value) {
				want, _ := json.Marshal(test.Value)
				t.Fatalf("source mismatch\ngot %s\nwant %s", raw, want)
			}
		})
	}
}
func TestHistoricalExposureScopedWriterRollbackCancellationAndNoRefresh(t *testing.T) {
	fixture := loadHistoricalOracle(t)
	test := fixture.Outcomes[0]
	s, f, c, e := historicalFixture(t, fixture.Fixed, test.Record, test.Proofs)
	ctx := context.Background()
	values, err := e.Query(ctx, c, epObj("memoryId", "history"))
	exposureMust(t, err)
	selected, err := e.Deliver(ctx, c, selectedMemory(values))
	exposureMust(t, err)
	if len(selected) != 1 {
		t.Fatal("historical receipt not delivered")
	}
	guard := e.CaptureGuard(c)
	// Guard validation must neither import nor append a revision or refresh a row.
	var before, after int
	exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&before))
	if !exposureCheck(t, f, guard) {
		t.Fatal("fresh historical guard invalid")
	}
	exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&after))
	if after != before {
		t.Fatal("scoped guard wrote", before, after)
	}
	rollback := errors.New("rollback")
	err = f.recovery.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		if _, err := ws.Exec("UPDATE config SET value=json_set(value,'$.memoryHistoricalReadEnabled',json('false')) WHERE key='settings'"); err != nil {
			return err
		}
		if err := f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			valid, err := guard.Check(reads)
			if valid || err != nil {
				t.Fatal("same-child history flag ignored", err)
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
		t.Fatal("rollback failed to restore guard")
	}
	var escaped botruntime.ReadAuthority
	exposureMust(t, f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { escaped = reads; return nil }))
	if valid, err := guard.Check(escaped); valid || err != nil {
		t.Fatal("expired scope accepted", err)
	}
	_, foreign := serviceSetup(t)
	exposureMust(t, foreign.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		valid, err := guard.Check(reads)
		if valid || err != nil {
			t.Fatal("foreign authority accepted", err)
		}
		return nil
	}))
	c.Signal().Abort(errors.New("old turn finished"))
	if !exposureCheck(t, f, guard) {
		t.Fatal("guard retained old cancellation")
	}
	_, err = s.Lock(ctx, "history", true)
	exposureMust(t, err)
	if exposureCheck(t, f, guard) {
		t.Fatal("new revision upgraded old snapshot")
	}
}
