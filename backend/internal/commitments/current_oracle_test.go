package commitments

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Recording ports isolate the actual source read-domain oracle. The production
// constructor does not inject this callback; separate native tests use the real
// RelationAuthority on one shared database scope.
func TestActualSourceCommitmentCurrentList(t *testing.T) {
	raw, e := os.ReadFile("testdata/read-source-oracle.json")
	must(t, e)
	root := parse(t, string(raw))
	for _, entry := range arr(get(root, "scenarios")) {
		t.Run(str(get(entry, "name")), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			must(t, f.store.Initialize(ctx))
			heads := arr(get(entry, "heads"))
			revisions := arr(get(entry, "revisions"))
			must(t, f.store.Transaction(ctx, func(s *Store) error {
				for _, h := range heads {
					if e := s.Put(object(h), nil); e != nil {
						return e
					}
				}
				for _, revision := range revisions {
					var head *Object
					for _, h := range heads {
						if get(h, "id") == get(get(revision, "ref"), "id") {
							head = object(h)
							break
						}
					}
					if e := s.Put(head, object(revision)); e != nil {
						return e
					}
				}
				return nil
			}))
			snapshot := func() string {
				return stringify(t, []any{rowsValue(t, f.main, "SELECT * FROM memory_commitment_heads ORDER BY rowid"), rowsValue(t, f.main, "SELECT * FROM memory_commitment_revisions ORDER BY rowid"), rowsValue(t, f.main, "SELECT * FROM memory_commitment_commands ORDER BY rowid")})
			}
			initial := snapshot()
			traces := arr(get(entry, "trace"))
			for i, op := range arr(get(entry, "ops")) {
				calls := []any{}
				c := Context{SceneID: "scene"}
				if v := get(op, "context"); v != nil {
					c = Context{SceneID: str(get(v, "sceneId")), Simulation: get(v, "simulation") == true}
				}
				contextValue := func(c Context) *Object { return obj("sceneId", c.SceneID, "simulation", c.Simulation) }
				current := func(c Context, r Reference) (*Assertion, error) {
					rv := obj("id", r.ID, "version", r.Version, "deletionEpoch", r.DeletionEpoch)
					calls = append(calls, []any{"current", contextValue(c), rv})
					if get(entry, "errorID") == r.ID {
						return nil, errors.New("synthetic_current_failure")
					}
					view := get(get(entry, "views"), r.ID)
					if view == nil {
						return nil, nil
					}
					expected := get(get(entry, "references"), r.ID)
					if expected != nil && !deepEqual(expected, rv) {
						return nil, nil
					}
					return &Assertion{Record: object(get(view, "record")), Proofs: arr(get(view, "proofs"))}, nil
				}
				var result *Object
				e := f.store.Read(ctx, func(reader *Reader) error {
					s := &ReadService{reader: reader, check: reader.CheckScope, current: current, enabled: func(c Context) (bool, error) {
						calls = append(calls, []any{"enabled", contextValue(c)})
						return get(entry, "enabled") == true, nil
					}}
					var err error
					switch str(get(op, "kind")) {
					case "current":
						result, err = s.Current(c, reference(get(op, "ref")))
					case "list":
						input := get(op, "options")
						options := ReadOptions{PersonID: str(get(input, "personId"))}
						if x := get(input, "limit"); x != nil {
							v := num(x)
							options.Limit = &v
						}
						if x := get(input, "lifecycle"); x != nil {
							v := str(x)
							options.Lifecycle = &v
						}
						result, err = s.List(c, options)
					case "project":
						result, err = ProjectCommitment(c, object(get(op, "head")), object(get(op, "revision")), current)
					default:
						t.Fatal("unknown fixture operation", op)
					}
					return err
				})
				expected := traces[i]
				if want := str(get(expected, "error")); want != "" {
					if e == nil || e.Error() != want {
						t.Fatalf("operation%d error %v want %s", i, e, want)
					}
				} else {
					must(t, e)
					if get(expected, "undefined") == true {
						if result != nil {
							t.Fatalf("operation%d expected undefined", i)
						}
					} else if !deepEqual(result, get(expected, "result")) {
						t.Fatalf("operation%d result\n%s\nwant\n%s", i, stringify(t, result), stringify(t, get(expected, "result")))
					}
				}
				// Compare exact property order as well as deep values for the result contract.
				if e == nil && result != nil {
					got := stringify(t, result)
					want := stringify(t, get(expected, "result"))
					if got != want {
						t.Fatalf("operation%d serialization\n%s\nwant\n%s", i, got, want)
					}
				}
				got, e := jsjson.Stringify(calls)
				must(t, e)
				want := stringify(t, get(expected, "calls"))
				if got != want {
					t.Fatalf("operation%d calls\n%s\nwant\n%s", i, got, want)
				}
				if snapshot() != initial {
					t.Fatalf("read operation%d changed retained database", i)
				}
			}
		})
	}
}
