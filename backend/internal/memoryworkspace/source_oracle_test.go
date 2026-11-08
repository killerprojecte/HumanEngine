package memoryworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"os"
	"path/filepath"
	"testing"
)

type recordingRead struct {
	input any
	calls []any
}

func (c *recordingRead) AssertReady() error { c.calls = append(c.calls, []any{"ready"}); return nil }
func contextValue(c commitments.Context) *Object {
	return obj("sceneId", c.SceneID, "simulation", c.Simulation)
}
func referenceValue(r commitments.Reference) *Object {
	return obj("id", r.ID, "version", r.Version, "deletionEpoch", r.DeletionEpoch)
}
func (c *recordingRead) CurrentAssertion(ctx commitments.Context, ref commitments.Reference) (*commitments.Assertion, error) {
	c.calls = append(c.calls, []any{"assertion", contextValue(ctx), referenceValue(ref)})
	v := get(get(c.input, "assertions"), ref.ID)
	if v == nil {
		return nil, nil
	}
	return &commitments.Assertion{Record: object(get(v, "record")), Proofs: arr(get(v, "proofs"))}, nil
}
func (c *recordingRead) Current(ctx commitments.Context, ref commitments.Reference) (*Object, error) {
	c.calls = append(c.calls, []any{"plan", contextValue(ctx), referenceValue(ref)})
	version, _ := jsjson.Stringify(ref.Version)
	return object(get(get(c.input, "plans"), ref.ID+":"+version)), nil
}
func (c *recordingRead) Heads(scene string) ([]any, error) {
	c.calls = append(c.calls, []any{"heads", scene})
	return arr(get(c.input, "heads")), nil
}
func (c *recordingRead) Enabled(ctx commitments.Context) (bool, error) {
	c.calls = append(c.calls, []any{"enabled", contextValue(ctx)})
	return get(c.input, "enabled") == true, nil
}
func (c *recordingRead) SourceHead(scene, message string) (*Object, error) {
	c.calls = append(c.calls, []any{"sourceHead", scene, message})
	return object(get(get(c.input, "sourceHeads"), scene+":"+message)), nil
}
func stringify(t *testing.T, v any) string {
	t.Helper()
	s, e := jsjson.Stringify(v)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestActualSourceWorkspaceResolve(t *testing.T) {
	raw, e := os.ReadFile("testdata/resolve-source-oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	root, e := jsjson.Parse(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range object(get(root, "sources")).Members() {
		b, e := os.ReadFile(filepath.Join("../../..", m.Key))
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != m.Value {
			t.Fatalf("stale source %s", m.Key)
		}
	}
	dates, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range arr(get(root, "scenarios")) {
		t.Run(str(get(entry, "name")), func(t *testing.T) {
			before := stringify(t, entry)
			read := &recordingRead{input: entry, calls: []any{}}
			resolver := &Resolver{read: read, dates: dates}
			refs := []workingset.Reference{}
			for _, v := range arr(get(entry, "refs")) {
				r := workingset.Reference{Kind: str(get(v, "kind")), ID: str(get(v, "id")), Version: num(get(v, "version")), DeletionEpoch: num(get(v, "deletionEpoch"))}
				if value, ok := get(v, "mutationVersion").(float64); ok {
					r.MutationVersion = &value
				}
				refs = append(refs, r)
			}
			context := get(entry, "context")
			result, e := resolver.Resolve(workingset.Context{SceneID: str(get(context, "sceneId")), Simulation: get(context, "simulation") == true}, refs)
			if want := get(entry, "error"); want != nil {
				if e == nil || e.Error() != want {
					t.Fatalf("got error %v want %v", e, want)
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				b, e := json.Marshal(result)
				if e != nil {
					t.Fatal(e)
				}
				v, e := jsjson.Parse(string(b))
				if e != nil {
					t.Fatal(e)
				}
				if got, want := stringify(t, v), stringify(t, get(entry, "value")); got != want {
					t.Fatalf("got %s\nwant %s", got, want)
				}
			}
			if got, want := stringify(t, read.calls), stringify(t, get(entry, "calls")); got != want {
				t.Fatalf("calls got %s\nwant %s", got, want)
			}
			if before != stringify(t, entry) {
				t.Fatal("input mutated")
			}
		})
	}
}
