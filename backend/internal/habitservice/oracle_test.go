package habitservice

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

func oracleContext(o *Object) Context {
	return Context{SceneID: str(o, "sceneId"), Simulation: truthy(get(o, "simulation")), SourceEvidence: arr(get(o, "sourceEvidence"))}
}
func oracleObjects(values []*Object) []any {
	r := make([]any, len(values))
	for i, v := range values {
		r[i] = v
	}
	return r
}

// Results permit only the tiny libm discrepancy in derived magnitudes. Raw
// persisted JSON and validation identities are compared byte-for-byte below.
func oracleEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	if a, ok := got.(float64); ok {
		if b, ok := want.(float64); ok && (a == b || math.Abs(a-b) <= 1e-14*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))) {
			return
		}
	}
	switch a := got.(type) {
	case []any:
		b, ok := want.([]any)
		if !ok || len(a) != len(b) {
			t.Fatalf("%s: got %#v want %#v", label, got, want)
		}
		for i := range a {
			oracleEqual(t, fmt.Sprintf("%s[%d]", label, i), a[i], b[i])
		}
		return
	case map[string]any:
		b, ok := want.(map[string]any)
		if !ok || len(a) != len(b) {
			t.Fatalf("%s: got %#v want %#v", label, got, want)
		}
		for k, v := range a {
			w, ok := b[k]
			if !ok {
				t.Fatalf("%s missing expected key %s", label, k)
			}
			oracleEqual(t, label+"."+k, v, w)
		}
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got %#v\nwant %#v", label, got, want)
	}
}
func oracleJSON(t *testing.T, v any) any {
	t.Helper()
	raw, e := jsjson.Stringify(v)
	if e != nil {
		t.Fatal(e)
	}
	var out any
	if e = json.Unmarshal([]byte(raw), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestActualSourceHabitServiceOracle(t *testing.T) {
	data, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	document := mustHabitObject(t, string(data))
	for _, entry := range arr(get(document, "cases")) {
		c := object(entry)
		t.Run(str(c, "name"), func(t *testing.T) {
			f := newHabitFixture(t)
			ctx := context.Background()
			initial := object(get(c, "initial"))
			for _, v := range arr(get(initial, "scenes")) {
				saveHabitScene(t, f, object(v))
			}
			p := DefaultPolicy()
			po := object(get(c, "policy"))
			if v, ok := po.Get("halfLifeDays"); ok {
				p.HalfLifeDays = num(v)
			}
			if v, ok := po.Get("retentionDays"); ok {
				p.RetentionDays = num(v)
			}
			if v, ok := po.Get("maxSources"); ok {
				p.MaxSources = num(v)
			}
			now := number(c, "now")
			serial := 0
			var armed *Object
			var callbackError error
			var service *Service
			var execute func(*Service, *Object) (any, error)
			options := Options{Policy: &p, ID: func() (string, error) { serial++; return fmt.Sprintf("habit-%d", serial), nil }, Now: func(bound *Service) float64 {
				a := armed
				armed = nil
				if a != nil {
					_, callbackError = execute(bound, a)
				}
				return now
			}}
			service, e = New(ctx, f.main, f.authority, options)
			if want := str(c, "constructorError"); want != "" {
				if e == nil || e.Error() != want {
					t.Fatalf("constructor: got %v want %s", e, want)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			put := func(r *Object) error {
				_, e := f.main.Exec("INSERT OR REPLACE INTO habits VALUES(?,?,?)", str(r, "id"), str(r, "fingerprint"), str(r, "value"))
				return e
			}
			for _, v := range arr(get(initial, "rows")) {
				if e := put(object(v)); e != nil {
					t.Fatal(e)
				}
			}
			for _, v := range arr(get(initial, "deleted")) {
				if _, e := f.main.Exec("INSERT INTO habit_deleted VALUES(?)", v); e != nil {
					t.Fatal(e)
				}
			}
			execute = func(s *Service, a *Object) (any, error) {
				hc := oracleContext(object(get(a, "ctx")))
				switch str(a, "method") {
				case "propose":
					return s.Propose(ctx, hc, object(get(a, "input")))
				case "feedback":
					return s.Feedback(ctx, hc, str(a, "id"), object(get(a, "input")))
				case "adminList":
					v, e := s.AdminList(ctx)
					return oracleObjects(v), e
				case "list":
					v, e := s.List(ctx, hc)
					return oracleObjects(v), e
				case "validationIdentity":
					v, e := s.ValidationIdentity(ctx, hc, str(a, "id"))
					if v == nil {
						return nil, e
					}
					return *v, e
				case "validationIdentities":
					ids := []string{}
					seen := map[string]bool{}
					for _, v := range arr(get(a, "ids")) {
						id := v.(string)
						if !seen[id] {
							ids = append(ids, id)
							seen[id] = true
						}
					}
					values, e := s.ValidationIdentities(ctx, hc, ids)
					out := []any{}
					for _, id := range ids {
						var value any
						if p := values[id]; p != nil {
							value = *p
						}
						out = append(out, []any{id, value})
					}
					return out, e
				case "update":
					return s.Update(ctx, str(a, "id"), object(get(a, "patch")))
				case "delete":
					return nil, s.Delete(ctx, str(a, "id"))
				case "recall":
					source := object(get(a, "source"))
					n, e := s.Recall(ctx, str(source, "sceneId"), str(source, "messageId"))
					return float64(n), e
				case "storeRecall":
					source := object(get(a, "source"))
					return nil, s.Transaction(ctx, func(b *Service) error {
						if _, e := b.session.AppendIntent(recovery.Source{SceneID: str(source, "sceneId"), MessageID: str(source, "messageId")}); e != nil {
							return e
						}
						_, e := b.session.Exec("INSERT OR IGNORE INTO recall_tombstones VALUES(?,?,?)", str(source, "sceneId"), str(source, "messageId"), "2026-11-02T00:00:00.000Z")
						return e
					})
				case "advance":
					now += number(a, "ms")
					return nil, nil
				case "restart":
					var e error
					service, e = New(ctx, f.main, f.authority, options)
					return nil, e
				case "armClock":
					armed = object(get(a, "action"))
					return nil, nil
				case "rawPut":
					return nil, put(object(get(a, "row")))
				case "rawDelete":
					_, e := f.main.Exec("DELETE FROM habits WHERE id=?", str(a, "id"))
					return nil, e
				case "saveScene":
					store := scenestore.Store{Main: f.main, Authority: f.authority}
					_, e := store.Save(ctx, object(get(a, "scene")))
					return nil, e
				case "sourceObserve":
					change, e := sourceevents.ParseChange(get(a, "change"))
					if e != nil {
						return nil, e
					}
					_, e = f.events.Observe(ctx, change)
					return nil, e
				default:
					return nil, fmt.Errorf("unknown action %s", str(a, "method"))
				}
			}
			for i, v := range arr(get(c, "steps")) {
				step := object(v)
				a := object(get(step, "action"))
				label := fmt.Sprintf("step %d %s", i, str(a, "method"))
				result, err := execute(service, a)
				if callbackError != nil {
					t.Fatalf("%s callback: %v", label, callbackError)
				}
				if want := str(step, "error"); want != "" {
					if err == nil || err.Error() != want {
						t.Fatalf("%s: got error %v want %s", label, err, want)
					}
				} else {
					if err != nil {
						t.Fatalf("%s: %v", label, err)
					}
					oracleEqual(t, label, oracleJSON(t, result), oracleJSON(t, get(step, "result")))
				}
				snapshot := object(get(step, "snapshot"))
				rows, e := f.main.Query("SELECT id,fingerprint,value FROM habits ORDER BY rowid")
				if e != nil {
					t.Fatal(e)
				}
				actual := []any{}
				for rows.Next() {
					var id, fp, value string
					if e = rows.Scan(&id, &fp, &value); e != nil {
						t.Fatal(e)
					}
					actual = append(actual, obj("id", id, "fingerprint", fp, "value", value))
				}
				if e = rows.Err(); e != nil {
					t.Fatal(e)
				}
				rows.Close()
				oracleEqual(t, label+" persisted rows", oracleJSON(t, actual), oracleJSON(t, get(snapshot, "rows")))
				rows, e = f.main.Query("SELECT fingerprint FROM habit_deleted ORDER BY rowid")
				if e != nil {
					t.Fatal(e)
				}
				deleted := []any{}
				for rows.Next() {
					var fp string
					if e = rows.Scan(&fp); e != nil {
						t.Fatal(e)
					}
					deleted = append(deleted, fp)
				}
				if e = rows.Err(); e != nil {
					t.Fatal(e)
				}
				rows.Close()
				oracleEqual(t, label+" deletion tombstones", oracleJSON(t, deleted), oracleJSON(t, get(snapshot, "deleted")))
			}
		})
	}
}
