package memoryservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

type commitmentOracleCase struct {
	Name                                         string
	Records, Proofs, Heads, Revisions, Timelines []json.RawMessage
	Value                                        map[string]any
}
type commitmentExposureOracle struct {
	Fixed    string
	Outcomes []commitmentOracleCase
}

func loadCommitmentExposureOracle(t *testing.T) commitmentExposureOracle {
	t.Helper()
	raw, err := os.ReadFile("testdata/commitment-exposure-source.json")
	exposureMust(t, err)
	var o commitmentExposureOracle
	exposureMust(t, json.Unmarshal(raw, &o))
	return o
}
func commitmentExposureFixture(t *testing.T, x commitmentOracleCase) (*Service, scopedFixture, *botruntime.TurnContext, *CommitmentExposure) {
	t.Helper()
	s, f := serviceSetup(t)
	c := exposureLiveTurn(t, s, f)
	ctx := context.Background()
	exposureWrite(t, f, "INSERT OR REPLACE INTO config(key,value) VALUES('settings',?)", `{"memoryCommitmentsEnabled":true,"memoryCommitmentsAnswerEnabled":true}`)
	records := make([]*Object, len(x.Records))
	proofs := make([][]any, len(x.Proofs))
	for i, raw := range x.Records {
		v, err := jsjson.Parse(string(raw))
		exposureMust(t, err)
		records[i] = v.(*Object)
		p, err := jsjson.Parse(string(x.Proofs[i]))
		exposureMust(t, err)
		proofs[i] = p.([]any)
		r := records[i]
		exposureWrite(t, f, "INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,'scene:s','p',?,?,?)", text(r, "id"), text(r, "content"), string(raw), text(r, "id"))
		exposureWrite(t, f, "INSERT INTO memory_supports(memory_id,value) VALUES(?,?)", text(r, "id"), string(x.Proofs[i]))
	}
	exposureMust(t, f.store.Transaction(ctx, func(tx *assertions.Store) error {
		for i, r := range records {
			if _, err := tx.ImportLegacy(r, proofs[i]); err != nil {
				return err
			}
		}
		return nil
	}))
	exposureMust(t, s.d.Commitments.Transaction(ctx, func(tx *commitments.Store) error {
		heads := map[string]*Object{}
		for _, raw := range x.Heads {
			h, err := jsjson.Parse(string(raw))
			if err != nil {
				return err
			}
			head := h.(*Object)
			heads[text(head, "id")] = head
			if err = tx.Put(head, nil); err != nil {
				return err
			}
		}
		for _, raw := range x.Revisions {
			r, err := jsjson.Parse(string(raw))
			if err != nil {
				return err
			}
			revision := r.(*Object)
			if err = tx.Put(heads[text(object(value(revision, "ref")), "id")], revision); err != nil {
				return err
			}
		}
		for _, raw := range x.Timelines {
			v, err := jsjson.Parse(string(raw))
			if err != nil {
				return err
			}
			if v == nil {
				continue
			}
			timeline := v.(*Object)
			if err = tx.ReplaceTimeline(text(object(value(timeline, "window")), "id"), timeline); err != nil {
				return err
			}
		}
		return nil
	}))
	port, err := NewExposurePort(s, f.events, f.main, f.recovery)
	exposureMust(t, err)
	e, err := NewCommitmentExposure(port)
	exposureMust(t, err)
	return s, f, c, e
}
func commitmentReceiptJSON(t *testing.T, v []*Object) json.RawMessage {
	t.Helper()
	raw, err := jsjson.Marshal(selectedMemory(v))
	exposureMust(t, err)
	return raw
}
func commitmentRefsJSON(t *testing.T, e *CommitmentExposure, c *botruntime.TurnContext) json.RawMessage {
	t.Helper()
	refs, err := e.ReadExposedRefs(context.Background(), c)
	exposureMust(t, err)
	v := []any{}
	for _, r := range refs {
		v = append(v, epObj("id", r.ID, "version", r.Version, "deletionEpoch", r.DeletionEpoch, "mutationVersion", r.MutationVersion))
	}
	raw, err := jsjson.Marshal(v)
	exposureMust(t, err)
	return raw
}
func TestCommitmentExposureActualSourceOracle(t *testing.T) {
	fixture := loadCommitmentExposureOracle(t)
	for _, test := range fixture.Outcomes {
		t.Run(test.Name, func(t *testing.T) {
			s, f, c, e := commitmentExposureFixture(t, test)
			ctx := context.Background()
			query := func(c *botruntime.TurnContext, input *Object) []*Object {
				v, err := e.Query(ctx, c, input)
				exposureMust(t, err)
				return v
			}
			deliver := func(c *botruntime.TurnContext, v []any) int {
				out, err := e.Deliver(ctx, c, v)
				exposureMust(t, err)
				return len(out)
			}
			var got any
			switch test.Name {
			case "whole_receipts_and_current_refs":
				values := query(c, nil)
				prepared := commitmentRefsJSON(t, e, c)
				forged := make([]any, len(values))
				for i, v := range values {
					forged[i] = exposureClone(v)
				}
				fake := deliver(c, forged)
				partial := deliver(c, selectedMemory(values[:1]))
				reverse := selectedMemory(values)
				reverse[0], reverse[1] = reverse[1], reverse[0]
				reversed := deliver(c, reverse)
				foreign := deliver(&botruntime.TurnContext{Scene: c.Scene, Mode: "real"}, selectedMemory(values))
				delivered := deliver(c, selectedMemory(values))
				repeat := deliver(c, selectedMemory(values))
				got = map[string]any{"values": commitmentReceiptJSON(t, values), "preparedRefs": prepared, "forged": fake, "partial": partial, "reversed": reversed, "foreign": foreign, "delivered": delivered, "repeat": repeat, "refs": commitmentRefsJSON(t, e, c), "dependencies": withoutTurnSource(c.Dependencies()), "valid": exposureCheck(t, f, e.CaptureGuard(c)), "simulation": len(query(&botruntime.TurnContext{Scene: c.Scene, Mode: "simulation"}, nil))}
			case "recall_invalidates_original_projection":
				values := query(c, nil)
				deliver(c, selectedMemory(values))
				guard := e.CaptureGuard(c)
				exposureWrite(t, f, "INSERT INTO recall_tombstones VALUES(?,?,?)", "s", "proposal-0", fixture.Fixed)
				got = map[string]any{"guard": exposureCheck(t, f, guard), "present": e.CaptureGuard(c) != nil, "refs": commitmentRefsJSON(t, e, c), "stale": deliver(c, selectedMemory(values)), "next": commitmentReceiptJSON(t, query(&botruntime.TurnContext{Scene: c.Scene, Mode: "real"}, nil)), "turnValid": exposureValid(t, f, c)}
			case "answer_flag_and_head_mutation_revoke":
				values := query(c, nil)
				deliver(c, selectedMemory(values))
				guard := e.CaptureGuard(c)
				exposureMust(t, s.d.Commitments.Transaction(ctx, func(tx *commitments.Store) error {
					return tx.Read(ctx, func(r *commitments.Reader) error {
						h, err := r.Head("plan-0")
						if err != nil {
							return err
						}
						_ = h.Set("mutationVersion", commitmentNumber(h, "mutationVersion")+1)
						return tx.Put(h, nil)
					})
				}))
				mutation := exposureCheck(t, f, guard)
				exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryCommitmentsAnswerEnabled',json('false')) WHERE key='settings'")
				got = map[string]any{"mutationValid": mutation, "guard": exposureCheck(t, f, guard), "present": e.CaptureGuard(c) != nil, "query": commitmentReceiptJSON(t, query(c, nil)), "refs": commitmentRefsJSON(t, e, c), "turnValid": exposureValid(t, f, c)}
			case "source_default_filters_and_empty":
				_, err := e.Query(ctx, c, epObj("lifecycle", "invalid"))
				if err == nil {
					t.Fatal("invalid lifecycle accepted")
				}
				got = map[string]any{"other": commitmentReceiptJSON(t, query(c, epObj("personId", "other"))), "terminal": commitmentReceiptJSON(t, query(c, epObj("lifecycle", "terminal"))), "all": commitmentReceiptJSON(t, query(c, epObj("lifecycle", "all"))), "ownUndefined": commitmentReceiptJSON(t, query(c, epObj("lifecycle", jsjson.Undefined))), "invalid": map[string]any{"error": err.Error()}}
			case "complete_page_required_at_source_limit":
				_, err := e.Query(ctx, c, nil)
				if err == nil {
					t.Fatal("limited page accepted")
				}
				got = map[string]any{"error": err.Error()}
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
func TestCommitmentExposureScopedWriterExpiryAndDetach(t *testing.T) {
	o := loadCommitmentExposureOracle(t)
	s, f, c, e := commitmentExposureFixture(t, o.Outcomes[0])
	ctx := context.Background()
	values, err := e.Query(ctx, c, nil)
	exposureMust(t, err)
	_, err = e.Deliver(ctx, c, selectedMemory(values))
	exposureMust(t, err)
	guard := e.CaptureGuard(c)
	var before, after int
	exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&before))
	if !exposureCheck(t, f, guard) {
		t.Fatal("initial guard")
	}
	exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&after))
	if before != after {
		t.Fatal("guard wrote")
	}
	rollback := errors.New("rollback")
	err = f.recovery.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		if _, err := ws.Exec("UPDATE config SET value=json_set(value,'$.memoryCommitmentsAnswerEnabled',json('false')) WHERE key='settings'"); err != nil {
			return err
		}
		if err := f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			valid, err := guard.Check(reads)
			if valid || err != nil {
				t.Fatal("writer flag ignored", err)
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
		t.Fatal("rollback guard")
	}
	var escaped botruntime.ReadAuthority
	exposureMust(t, f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		escaped = reads
		return f.events.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			var retained *commitments.ReadService
			err := s.d.Commitments.ReadRuntimeCurrent(scope, s.d.Assertions, s.d.CommitmentsEnabled, s.d.Authority, func(r *commitments.ReadService) error { retained = r; return nil })
			if err != nil {
				return err
			}
			_, err = retained.Current(commitments.Context{SceneID: "s"}, commitments.Reference{ID: "plan-0", Version: 1})
			if !errors.Is(err, sql.ErrTxDone) {
				t.Fatal("retained read service escaped", err)
			}
			return nil
		})
	}))
	if valid, err := guard.Check(escaped); valid || err != nil {
		t.Fatal("expired guard accepted", err)
	}
	_, foreign := serviceSetup(t)
	exposureMust(t, foreign.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		valid, err := guard.Check(reads)
		if valid || err != nil {
			t.Fatal("foreign guard accepted", err)
		}
		return nil
	}))
	c.Signal().Abort(errors.New("turn finished"))
	if !exposureCheck(t, f, guard) {
		t.Fatal("guard retained old turn cancellation")
	}
}
