package projections

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type sourceScenario struct {
	Name    string                     `json:"name"`
	States  []map[string]any           `json:"states"`
	Records map[string]json.RawMessage `json:"records"`
	Enabled bool                       `json:"enabled"`
	Compact bool                       `json:"compact"`
	Steps   []map[string]any           `json:"steps"`
	Trace   []map[string]any           `json:"trace"`
}

func convert(t *testing.T, from, to any) {
	t.Helper()
	raw, e := json.Marshal(from)
	must(t, e)
	must(t, json.Unmarshal(raw, to))
}
func number(v any) int64           { n, _ := v.(float64); return int64(n) }
func boolean(v any) bool           { b, _ := v.(bool); return b }
func mapping(v any) map[string]any { m, _ := v.(map[string]any); return m }
func oracleRows(t *testing.T, db *sql.DB, q string, args ...any) ([]map[string]any, string) {
	t.Helper()
	rows, e := db.Query(q, args...)
	must(t, e)
	columns, e := rows.Columns()
	must(t, e)
	result := []map[string]any{}
	ordered := []any{}
	for rows.Next() {
		values := make([]any, len(columns))
		p := make([]any, len(columns))
		for i := range values {
			p[i] = &values[i]
		}
		must(t, rows.Scan(p...))
		item := map[string]any{}
		o := new(Object)
		for i, k := range columns {
			v := values[i]
			if n, ok := v.(int64); ok {
				v = float64(n)
			}
			item[k] = v
			must(t, o.Set(k, v))
		}
		result = append(result, item)
		ordered = append(ordered, o)
	}
	must(t, errors.Join(rows.Err(), rows.Close()))
	raw, e := jsjson.Stringify(ordered)
	must(t, e)
	return result, raw
}
func oracleInspect(t *testing.T, f *fixture, compact bool) map[string]any {
	rows, raw := oracleRows(t, f.db, "SELECT * FROM memory_projection_outbox ORDER BY id")
	var outbox any = rows
	if compact {
		hash := sha256.Sum256([]byte(raw))
		first, last := rows, rows
		if len(rows) > 3 {
			first = rows[:3]
			last = rows[len(rows)-3:]
		}
		counts := map[string]int{"pending": 0, "leased": 0, "done": 0, "invalid": 0, "failed": 0}
		for _, row := range rows {
			counts[str(row["state"])]++
		}
		outbox = map[string]any{"count": len(rows), "sha256": hex.EncodeToString(hash[:]), "first": first, "last": last, "states": counts}
	}
	fts, _ := oracleRows(t, f.db, "SELECT rowid,assertion_id,scope_key,person_id,content FROM memory_fts ORDER BY rowid")
	person, _ := oracleRows(t, f.db, "SELECT * FROM memory_person_projection_state ORDER BY assertion_id")
	effects, _ := oracleRows(t, f.db, "SELECT * FROM fixture_effect ORDER BY id")
	states, _ := oracleRows(t, f.db, "SELECT * FROM memory_assertion_state ORDER BY assertion_id")
	return map[string]any{"outbox": outbox, "fts": fts, "person": person, "effects": effects, "assertions": states}
}
func TestActualSourceProjectionTraces(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-oracle.json")
	must(t, e)
	var oracle struct {
		Scenarios []sourceScenario `json:"scenarios"`
	}
	must(t, json.Unmarshal(raw, &oracle))
	for _, scenario := range oracle.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			f.now = 10000
			enabled := scenario.Enabled
			calls := []map[string]any{}
			token := 0
			faults := map[string]string{}
			records := map[string]*Object{}
			leases := []Lease{}
			for id, raw := range scenario.Records {
				v, e := jsjson.ParseBytes(raw)
				must(t, e)
				records[id], _ = v.(*Object)
			}
			_, e = f.db.Exec("CREATE TABLE fixture_effect(id TEXT PRIMARY KEY,value TEXT)")
			must(t, e)
			writeState := func(b *Outbox, v map[string]any) error {
				q := `INSERT INTO memory_assertion_state VALUES(?,?,?,?) ON CONFLICT(assertion_id) DO UPDATE SET version=excluded.version,deletion_epoch=excluded.deletion_epoch,deleted=excluded.deleted`
				args := []any{v["assertion_id"], v["version"], v["deletion_epoch"], v["deleted"]}
				if b != nil {
					_, e := b.exec(q, args...)
					return e
				}
				_, e := f.db.Exec(q, args...)
				return e
			}
			for _, s := range scenario.States {
				must(t, writeState(nil, s))
			}
			options := Options{Now: func() float64 { calls = append(calls, map[string]any{"op": "now", "value": f.now}); return f.now }, Token: func() (string, error) {
				token++
				value := fmt.Sprintf("00000000-0000-4000-8000-%012d", token)
				calls = append(calls, map[string]any{"op": "token", "value": value})
				return value, nil
			}}
			var c *Coordinator
			initialize := func() {
				var e error
				c, e = New(ctx, f.db, f.authority, func() bool { calls = append(calls, map[string]any{"op": "enabled", "value": enabled}); return enabled }, func(_ *recovery.WriteSession, id string) (*Object, error) {
					calls = append(calls, map[string]any{"op": "eligible", "id": id})
					if faults[id] != "" {
						return nil, errors.New(faults[id])
					}
					return records[id], nil
				}, options)
				must(t, e)
				f.outbox = c.outbox
			}
			initialize()
			for index, step := range scenario.Steps {
				calls = []map[string]any{}
				var result any
				defined := false
				var actionError error
				action := func(bound *Outbox) error {
					owner := c
					outbox := c.outbox
					if bound != nil {
						owner = c.WithSession(bound.session)
						outbox = owner.outbox
					}
					lease := Lease{}
					if value, ok := step["leaseIndex"]; ok {
						lease = leases[int(number(value))]
						if override, ok := step["override"]; ok {
							var fields map[string]any
							convert(t, lease, &fields)
							for k, v := range mapping(override) {
								fields[k] = v
							}
							convert(t, fields, &lease)
						}
					}
					var snapshot Snapshot
					if v, ok := step["snapshot"]; ok {
						convert(t, v, &snapshot)
					}
					switch str(step["op"]) {
					case "enqueue":
						defined = true
						var e error
						result, e = outbox.Enqueue(snapshot)
						return e
					case "claim":
						defined = true
						found, e := outbox.Claim(ctx, number(step["leaseMs"]), int(number(step["limit"])))
						if e == nil {
							leases = append(leases, found...)
						}
						result = found
						return e
					case "complete":
						defined = true
						value, e := outbox.Complete(ctx, lease, func(child *Outbox) error {
							calls = append(calls, map[string]any{"op": "apply"})
							a := mapping(step["callback"])
							if effect := mapping(a["effect"]); effect != nil {
								if _, e := child.exec("INSERT INTO fixture_effect VALUES(?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", effect["id"], effect["value"]); e != nil {
									return e
								}
							}
							if state := mapping(a["state"]); state != nil {
								if e := writeState(child, state); e != nil {
									return e
								}
							}
							if value, ok := a["clock"]; ok {
								f.now = value.(float64)
							}
							if id := str(a["invalidate"]); id != "" {
								if e := child.Invalidate(id); e != nil {
									return e
								}
							}
							if message := str(a["error"]); message != "" {
								return errors.New(message)
							}
							return nil
						})
						result = value
						return e
					case "fail":
						defined = true
						value, e := outbox.Fail(ctx, lease, str(step["reason"]))
						result = value
						return e
					case "invalidate":
						return outbox.Invalidate(str(step["id"]))
					case "prune":
						defined = true
						value, e := outbox.PruneTerminal(ctx, int(number(step["limit"])))
						result = value
						return e
					case "committed":
						return owner.Committed(snapshot.AssertionID, snapshot.Version, snapshot.DeletionEpoch)
					case "flush":
						value, e := owner.Flush(ctx)
						if value != nil {
							defined = true
							result = *value
						}
						return e
					case "drain":
						defined = true
						value, e := owner.processor.Drain(ctx, int(number(step["limit"])))
						result = value
						return e
					case "remove":
						return owner.Remove(str(step["id"]))
					case "state":
						return writeState(bound, mapping(step["value"]))
					case "record":
						raw, e := json.Marshal(step["value"])
						if e != nil {
							return e
						}
						v, e := jsjson.ParseBytes(raw)
						if e != nil {
							return e
						}
						records[str(step["id"])], _ = v.(*Object)
						return nil
					case "eligibleFault":
						faults[str(step["id"])] = str(step["value"])
						return nil
					case "clock":
						f.now = step["value"].(float64)
						return nil
					case "flag":
						enabled = boolean(step["value"])
						return nil
					case "sql":
						if bound != nil {
							_, e := bound.exec(str(step["statement"]))
							return e
						}
						_, e := f.db.Exec(str(step["statement"]))
						return e
					case "reopen":
						must(t, f.db.Close())
						f.db, e = sql.Open("sqlite3", f.path+"?_foreign_keys=on")
						must(t, e)
						f.db.SetMaxOpenConns(1)
						initialize()
						return nil
					case "match":
						defined = true
						q := "SELECT assertion_id,scope_key,person_id,content FROM memory_fts WHERE memory_fts MATCH ?"
						args := []any{step["query"]}
						if scope, ok := step["scope"]; ok {
							q += " AND scope_key=?"
							args = append(args, scope)
						}
						if person, ok := step["person"]; ok {
							q += " AND person_id IS ?"
							args = append(args, person)
						}
						result, _ = oracleRows(t, f.db, q+" ORDER BY assertion_id", args...)
						return nil
					case "seedRetention":
						return outbox.Transaction(ctx, func(b *Outbox) error {
							insert := func(id, state string) error {
								_, e := b.exec(`INSERT INTO memory_projection_outbox(assertion_id,version,deletion_epoch,kind,state,available_at,updated_at) VALUES(?,1,0,'lexical',?,10000,10000)`, id, state)
								return e
							}
							if e := insert("a", "done"); e != nil {
								return e
							}
							states := []string{"done", "invalid", "failed"}
							for i := 0; i < 4100; i++ {
								if e := insert(fmt.Sprintf("old-%d", i), states[i%3]); e != nil {
									return e
								}
							}
							if e := insert("pending", "pending"); e != nil {
								return e
							}
							return insert("leased", "leased")
						})
					default:
						return fmt.Errorf("unknown projection step %s", step["op"])
					}
				}
				op := str(step["op"])
				outer := boolean(step["outer"]) || boolean(step["rollback"]) || ((op == "enqueue" || op == "committed" || op == "remove" || op == "invalidate") && step["transaction"] != false)
				if outer {
					rollback := errors.New("oracle_outer_rollback")
					e := c.outbox.Transaction(ctx, func(b *Outbox) error {
						actionError = action(b)
						if boolean(step["rollback"]) {
							return rollback
						}
						return nil
					})
					if e != nil && !errors.Is(e, rollback) {
						t.Fatal(e)
					}
				} else {
					actionError = action(nil)
				}
				actual := map[string]any{"calls": calls, "db": oracleInspect(t, f, scenario.Compact)}
				if actionError != nil {
					actual["error"] = actionError.Error()
				} else if defined {
					actual["result"] = result
				} else {
					actual["undefined"] = true
				}
				var normalized map[string]any
				convert(t, actual, &normalized)
				if !reflect.DeepEqual(normalized, scenario.Trace[index]) {
					got, _ := json.MarshalIndent(normalized, "", "  ")
					want, _ := json.MarshalIndent(scenario.Trace[index], "", "  ")
					t.Fatalf("step %d %v\ngot %s\nwant %s", index, step, got, want)
				}
			}
		})
	}
}
