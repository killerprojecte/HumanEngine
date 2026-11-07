package workingset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func object(v any) *jsjson.Object { o, _ := v.(*jsjson.Object); return o }
func field(v any, k string) any {
	if o := object(v); o != nil {
		x, _ := o.Get(k)
		return x
	}
	return nil
}
func stringField(v any, k string) string { x, _ := field(v, k).(string); return x }
func boolField(v any, k string) bool     { x, _ := field(v, k).(bool); return x }
func numberField(v any, k string, def float64) float64 {
	x, ok := field(v, k).(float64)
	if !ok {
		return def
	}
	return x
}
func array(v any) []any { a, _ := v.([]any); return a }
func obj(kv ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func typed[T any](t *testing.T, raw any) T {
	t.Helper()
	var out T
	must(t, decodeValue(raw, reflect.ValueOf(&out).Elem()))
	return out
}
func schemaCanonical(name, raw string) (string, error) {
	switch name {
	case "WorkingSetReferenceSchema":
		v, e := Decode[Reference](raw)
		if e != nil {
			return "", e
		}
		return Encode(v)
	case "WorkingSetSelectionSchema":
		v, e := Decode[Selection](raw)
		if e != nil {
			return "", e
		}
		return Encode(v)
	case "WorkingSetSnapshotSchema":
		v, e := Decode[Snapshot](raw)
		if e != nil {
			return "", e
		}
		return Encode(v)
	case "WorkingSetPinPolicySchema":
		v, e := Decode[PinPolicy](raw)
		if e != nil {
			return "", e
		}
		return Encode(v)
	case "WorkingSetItemSchema":
		v, e := Decode[Item](raw)
		if e != nil {
			return "", e
		}
		return Encode(v)
	case "WorkingSetJobSchema":
		v, e := Decode[Job](raw)
		if e != nil {
			return "", e
		}
		return Encode(v)
	case "WorkingSetReceiptSchema":
		v, e := Decode[Receipt](raw)
		if e != nil {
			return "", e
		}
		return Encode(v)
	}
	return "", errors.New("unknown schema")
}
func TestActualSourceSchemas(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-oracle.json")
	must(t, e)
	root, e := jsjson.ParseBytes(raw)
	must(t, e)
	for i, row := range array(field(root, "schemas")) {
		t.Run(fmt.Sprintf("%d_%s", i, stringField(row, "schema")), func(t *testing.T) {
			input, e := jsjson.Stringify(field(row, "input"))
			must(t, e)
			canonical, e := schemaCanonical(stringField(row, "schema"), input)
			if (e == nil) != boolField(row, "accepted") {
				t.Fatalf("accepted mismatch: %s: %v", input, e)
			}
			if e == nil && canonical != stringField(row, "canonical") {
				t.Fatalf("canonical\ngot %s\nwant %s", canonical, stringField(row, "canonical"))
			}
		})
	}
	for _, row := range array(field(root, "storageSchemas")) {
		t.Run(stringField(row, "name"), func(t *testing.T) {
			f := setup(t)
			_, e := f.db.Exec(stringField(row, "sql"))
			must(t, e)
			var supported bool
			e = f.service.Transaction(context.Background(), func(s *Service) error { var e error; supported, e = s.Storage().AssertSupported(); return e })
			if wanted := stringField(row, "error"); wanted != "" {
				if e == nil || !strings.Contains(e.Error(), wanted) {
					t.Fatalf("got %v want %s", e, wanted)
				}
			} else if e != nil || supported != boolField(row, "supported") {
				t.Fatal(supported, e)
			}
		})
	}
}
func TestActualSourceTraces(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-oracle.json")
	must(t, e)
	root, e := jsjson.ParseBytes(raw)
	must(t, e)
	for _, scenario := range array(field(root, "scenarios")) {
		t.Run(stringField(scenario, "name"), func(t *testing.T) { runOracle(t, scenario) })
	}
}
func runOracle(t *testing.T, input any) {
	f := setup(t)
	ctx := context.Background()
	f.now = numberField(input, "now", 0)
	f.enabled = boolField(input, "enabled")
	f.snapshots = typed[[]Snapshot](t, field(input, "snapshots"))
	policy := typed[Policy](t, field(input, "policy"))
	calls := []any{}
	pins := []PinPolicy{}
	leases := []Lease{}
	faults := map[string]string{}
	mode := ""
	resolveSQL := ""
	call := func(kv ...any) { calls = append(calls, obj(kv...)) }
	fault := func(name string) error {
		if faults[name] != "" {
			return errors.New(faults[name])
		}
		return nil
	}
	resolve := func(scope *Scope, c Context, refs []Reference) ([]Snapshot, error) {
		cc, _ := encodeValue(reflect.ValueOf(c))
		rr, _ := encodeValue(reflect.ValueOf(refs))
		call("op", "resolve", "ctx", cc, "refs", rr)
		if e := fault("resolve"); e != nil {
			return nil, e
		}
		if resolveSQL != "" {
			q := resolveSQL
			resolveSQL = ""
			if _, e := scope.WriteSession().Exec(q); e != nil {
				return nil, e
			}
		}
		out := []Snapshot{}
		for _, s := range f.snapshots {
			for _, r := range refs {
				if ReferenceKey(r) == ReferenceKey(s.Ref) {
					out = append(out, s)
					break
				}
			}
		}
		if mode == "duplicate" && len(out) > 0 {
			return []Snapshot{out[0], out[0]}, nil
		}
		return out, nil
	}
	ports := Ports{Policy: &policy, ObserveReady: func(_ *Scope) error { call("op", "ready"); return fault("ready") }, Enabled: func() bool { call("op", "enabled", "value", f.enabled); return f.enabled }, Now: func() float64 { call("op", "now", "value", f.now); return f.now }, Token: func() (string, error) {
		f.tokens++
		v := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.tokens)
		call("op", "token", "value", v)
		return v, nil
	}, ResolveCurrent: resolve, BeforeWrite: func(_ *recovery.WriteSession) error { call("op", "beforeWrite"); return fault("write") }, ObservePinWrite: func(_ *recovery.WriteSession, p PinPolicy) error {
		value, _ := encodeValue(reflect.ValueOf(p))
		call("op", "beforePinWrite", "policy", value)
		if e := fault("pin"); e != nil {
			return e
		}
		pins = append(pins, p)
		return nil
	}}
	var e error
	f.service, e = New(ctx, f.db, f.authority, ports)
	must(t, e)
	mutations := func(s *Service) *MutationHooks {
		return s.Mutations(MutationPorts{ResolveCommitted: func(session *recovery.WriteSession, c Context, refs []Reference) ([]Snapshot, error) {
			return resolve(s.WithSession(session).scope(), c, refs)
		}, AffectedAssertionScenes: func(session *recovery.WriteSession, id string, after *string, limit int) ([]string, error) {
			var cursor any
			if after != nil {
				cursor = *after
			}
			call("op", "affectedScenes", "id", id, "after", cursor, "limit", float64(limit))
			return s.WithSession(session).Storage().AffectedAssertionScenes(id, after, limit)
		}})
	}
	steps := array(field(input, "steps"))
	traces := array(field(input, "trace"))
	if len(steps) != len(traces) {
		t.Fatal("trace count")
	}
	for index, step := range steps {
		calls = []any{}
		c := Context{SceneID: "scene"}
		if x := field(input, "context"); x != nil {
			c = typed[Context](t, x)
		}
		if x := field(step, "ctx"); x != nil {
			c = typed[Context](t, x)
		}
		op := stringField(step, "op")
		var result any
		undefined := false
		act := func(s *Service) error {
			var e error
			switch op {
			case "view":
				cold := true
				if x := field(field(step, "options"), "includeCold"); x != nil {
					cold = x.(bool)
				}
				result, e = s.View(ctx, c, typed[[]Selection](t, field(step, "selections")), cold)
			case "recordSelected":
				result, e = s.RecordSelected(ctx, c, typed[SelectedInput](t, field(step, "input")))
			case "listPinPolicies":
				result, e = s.ListPinPolicies(ctx, c, numberField(field(step, "options"), "limit", 100))
			case "setPinned":
				in := field(step, "input")
				result, e = s.SetPinned(ctx, c, typed[Selection](t, field(step, "selection")), numberField(in, "expectedRevision", 0), boolField(in, "pinned"))
			case "enqueue":
				result, e = s.Enqueue(ctx, c, typed[[]Selection](t, field(step, "selections")))
			case "claim":
				var got []Lease
				got, e = s.Claim(ctx, c, numberField(step, "limit", s.policy.load().MaxBatch))
				result = got
				if e == nil {
					leases = append(leases, got...)
				}
			case "complete":
				selected := []Lease{}
				if value := field(step, "leases"); value != nil {
					selected = typed[[]Lease](t, value)
				} else {
					for _, i := range array(field(step, "indexes")) {
						l := leases[int(i.(float64))]
						if overrides := object(field(step, "override")); overrides != nil {
							v, _ := encodeValue(reflect.ValueOf(l))
							for _, member := range overrides.Members() {
								_ = object(v).Set(member.Key, member.Value)
							}
							l = typed[Lease](t, v)
						}
						selected = append(selected, l)
					}
				}
				result, e = s.Complete(ctx, c, selected)
			case "drain":
				result, e = s.Drain(ctx, c, numberField(step, "limit", s.policy.load().MaxBatch))
			case "cancelMaintenance":
				var scene *string
				if value := field(step, "sceneId"); value != nil {
					x := value.(string)
					scene = &x
				}
				e = s.CancelMaintenance(ctx, scene)
				undefined = true
			case "invalidate", "terminalInvalidate":
				scene := stringField(step, "sceneId")
				if scene == "" {
					scene = c.SceneID
				}
				refs := []Entity{}
				for _, raw := range array(field(step, "refs")) {
					refs = append(refs, Entity{stringField(raw, "kind"), stringField(raw, "id")})
				}
				if op == "invalidate" {
					e = s.Invalidate(ctx, scene, refs)
				} else {
					e = s.TerminalInvalidate(ctx, scene, refs)
				}
				undefined = true
			case "configurePolicy":
				var cold, review *float64
				for _, m := range object(field(step, "value")).Members() {
					v, ok := m.Value.(float64)
					if !ok {
						return errors.New("working_set_policy_invalid")
					}
					switch m.Key {
					case "coldAfterMs":
						cold = &v
					case "reviewAfterMs":
						review = &v
					default:
						return errors.New("working_set_policy_invalid")
					}
				}
				e = s.ConfigurePolicy(cold, review)
				undefined = true
			case "committed":
				result, e = mutations(s).Committed(c, typed[[]Reference](t, field(step, "refs")))
			case "sourceEdited":
				result, e = mutations(s).SourceEdited(c, typed[[]Reference](t, field(step, "refs")))
			case "invalidateAssertion":
				result, e = mutations(s).InvalidateAssertion(stringField(step, "id"))
			case "terminalAssertionDeleted":
				result, e = mutations(s).TerminalAssertionDeleted(stringField(step, "id"))
			case "clock":
				f.now = numberField(step, "value", 0)
				undefined = true
			case "flag":
				f.enabled = boolField(step, "value")
				undefined = true
			case "snapshots":
				f.snapshots = typed[[]Snapshot](t, field(step, "value"))
				undefined = true
			case "fault":
				faults[stringField(step, "hook")] = stringField(step, "value")
				undefined = true
			case "resolveMode":
				mode = stringField(step, "value")
				undefined = true
			case "resolveSQL":
				resolveSQL = stringField(step, "value")
				undefined = true
			case "seedScenes":
				e = s.Transaction(ctx, func(b *Service) error {
					if e := b.Storage().Ensure(); e != nil {
						return e
					}
					for i := 0; i < int(numberField(step, "count", 0)); i++ {
						scene := fmt.Sprintf("scene-%03d", i)
						snapshot := snap("a", scene)
						if e := b.Storage().PutPin(PinPolicy{SchemaVersion: 1, SceneID: scene, Selection: snapshot.Selection(), PolicyRevision: 1, Pinned: true}); e != nil {
							return e
						}
						if e := b.Storage().PutItem(makeItem(snapshot, nil, f.now)); e != nil {
							return e
						}
					}
					return nil
				})
				undefined = true
			case "sql":
				_, e = f.db.Exec(stringField(step, "value"))
				undefined = true
			case "reopen":
				f.service, e = New(ctx, f.db, f.authority, ports)
				undefined = true
			default:
				return fmt.Errorf("unknown_fixture_op:%s", op)
			}
			return e
		}
		mutation := oneOf(op, "committed", "sourceEdited", "invalidateAssertion", "terminalAssertionDeleted")
		if boolField(step, "outer") || boolField(step, "rollback") || (mutation && !boolField(step, "outside")) {
			rolled := errors.New("fixture_outer_rollback")
			e = f.service.Transaction(ctx, func(s *Service) error {
				if e := act(s); e != nil {
					return e
				}
				if boolField(step, "rollback") {
					return rolled
				}
				return nil
			})
			if errors.Is(e, rolled) {
				e = nil
			}
		} else {
			e = act(f.service)
		}
		expected := traces[index]
		prefix := fmt.Sprintf("step %d %s", index, op)
		if wanted := stringField(expected, "error"); wanted != "" {
			if e == nil || e.Error() != wanted {
				t.Fatalf("%s error got %v want %s", prefix, e, wanted)
			}
		} else {
			must(t, e)
			if undefined != boolField(expected, "undefined") {
				t.Fatalf("%s undefined got %v", prefix, undefined)
			}
			if !undefined {
				got, e := JSON(result)
				must(t, e)
				want, e := jsjson.Stringify(field(expected, "result"))
				must(t, e)
				compareJSON(t, prefix+" result", got, want)
			}
		}
		gotCalls, e := jsjson.Stringify(calls)
		must(t, e)
		wantCalls, e := jsjson.Stringify(field(expected, "calls"))
		must(t, e)
		compareJSON(t, prefix+" calls", gotCalls, wantCalls)
		gotPins, e := JSON(pins)
		must(t, e)
		wantPins, e := jsjson.Stringify(field(expected, "pins"))
		must(t, e)
		compareJSON(t, prefix+" pins", gotPins, wantPins)
		db := new(jsjson.Object)
		for _, table := range []string{"memory_working_set_pins", "memory_working_set_items", "memory_working_set_jobs", "memory_working_set_receipts"} {
			var n int
			must(t, f.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=? AND type='table'", table).Scan(&n))
			var rowsValue any
			if n > 0 {
				rowsValue = []any{}
				rows, e := f.db.Query("SELECT * FROM " + table + " ORDER BY rowid")
				must(t, e)
				columns, e := rows.Columns()
				must(t, e)
				for rows.Next() {
					values := make([]any, len(columns))
					dest := make([]any, len(columns))
					for i := range values {
						dest[i] = &values[i]
					}
					must(t, rows.Scan(dest...))
					o := new(jsjson.Object)
					for i, v := range values {
						if n, ok := v.(int64); ok {
							v = float64(n)
						}
						_ = o.Set(columns[i], v)
					}
					rowsValue = append(rowsValue.([]any), o)
				}
				must(t, rows.Err())
				must(t, rows.Close())
			}
			_ = db.Set(table, rowsValue)
		}
		gotDB, e := jsjson.Stringify(db)
		must(t, e)
		wantDB, e := jsjson.Stringify(field(expected, "db"))
		must(t, e)
		compareJSON(t, prefix+" db", gotDB, wantDB)
	}
}
func compareJSON(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s\ngot  %s\nwant %s", label, got, want)
	}
}
