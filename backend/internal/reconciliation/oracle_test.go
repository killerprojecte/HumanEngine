package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"os"
	"path/filepath"
	"testing"
)

func resultValue(r Result) *Object {
	v := obj("status", r.Status)
	if r.Status == "rejected" {
		_ = v.Set("reason", r.Reason)
		return v
	}
	_ = v.Set("fp", r.FP)
	if r.ExistingRow != nil {
		b, _ := json.Marshal(r.ExistingRow)
		row, _ := jsjson.Parse(string(b))
		_ = v.Set("existingRow", row)
	}
	if r.Superseded != nil {
		_ = v.Set("superseded", r.Superseded)
	}
	return v
}
func seed(t *testing.T, session *recovery.WriteSession, input *Object) {
	t.Helper()
	for _, r := range arr(get(input, "rows")) {
		_, e := session.Exec("INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)", get(r, "id"), get(r, "scope_key"), get(r, "person_id"), get(r, "content"), get(r, "fingerprint"), get(r, "value"))
		if e != nil {
			t.Fatal(e)
		}
	}
	if proofs := object(get(input, "proofs")); proofs != nil {
		for _, m := range proofs.Members() {
			raw, e := jsjson.Stringify(m.Value)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = session.Exec("INSERT INTO memory_supports VALUES(?,?)", m.Key, raw); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, fp := range arr(get(input, "deleted")) {
		if _, e := session.Exec("INSERT INTO memory_deleted VALUES(?,'now')", fp); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range arr(get(input, "recalled")) {
		if _, e := session.Exec("INSERT INTO recall_tombstones VALUES(?,?,'now')", get(s, "sceneId"), get(s, "messageId")); e != nil {
			t.Fatal(e)
		}
	}
}
func inspectInput(p *Policy, input *Object) (Result, error) {
	return p.Inspect(object(get(input, "ctx")), object(get(input, "candidate")), object(get(input, "scope")), object(get(input, "attribution")), arr(get(input, "parents")))
}
func TestActualSourceOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/reconciliation.json")
	if e != nil {
		t.Fatal(e)
	}
	var data struct {
		Node, Timezone string
		Sources        map[string]string
		Cases          []struct{ Name, Input, Expected string }
	}
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	if data.Node != "v24.22.1" || data.Timezone != "UTC" {
		t.Fatal("unexpected source runtime")
	}
	required := []string{"src/memory/write/reconciliation-policy.ts", "src/memory/storage/assertion-repository.ts", "src/memory/domain/record-values.ts", "src/memory/domain/proofs.ts", "package-lock.json", "backend/tests/generate-reconciliation-fixtures.mjs"}
	if len(data.Sources) != len(required) {
		t.Fatal("wrong source hash set")
	}
	for _, p := range required {
		b, e := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(p)))
		if e != nil {
			t.Fatal(e)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != data.Sources[p] {
			t.Fatalf("source drift: %s; regenerate actual-source oracle", p)
		}
	}
	if len(data.Cases) < 95 {
		t.Fatal("missing source cases")
	}
	for _, c := range data.Cases {
		t.Run(c.Name, func(t *testing.T) {
			input := parseObject(t, c.Input)
			expected := parseObject(t, c.Expected)
			f := scopedSetup(t)
			ctx := context.Background()
			e := f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
				seed(t, session, input)
				return f.store.WithSession(session).Read(ctx, func(reader *assertions.Reader) error {
					p, e := New(reader, testOptions(t))
					if e != nil {
						return e
					}
					result, e := inspectInput(p, input)
					if e != nil {
						return e
					}
					got := resultValue(result)
					if !deepEqual(got, expected) {
						raw, _ := jsjson.Stringify(got)
						t.Fatalf("got %s\nwant %s", raw, c.Expected)
					}
					return nil
				})
			})
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}
