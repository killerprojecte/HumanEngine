package memoryservice

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionproposal"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/relations"
	"os"
	"path/filepath"
	"testing"
)

func TestOuterProposalActualSourceOrchestration(t *testing.T) {
	raw, e := os.ReadFile("testdata/outer-proposal-source.json")
	if e != nil {
		t.Fatal(e)
	}
	rootValue, e := jsjson.ParseBytes(raw)
	if e != nil {
		t.Fatal(e)
	}
	root := object(rootValue)
	manifest := object(value(root, "sources"))
	if manifest.Len() != 5 {
		t.Fatal("source manifest count")
	}
	for _, path := range []string{"src/memory/write/proposal-command.ts", "src/memory/knowledge/admission.ts", "src/memory/domain/record-values.ts", "package-lock.json", "backend/tests/generate-outer-proposal-fixtures.mjs"} {
		b, e := os.ReadFile(filepath.Join("../../..", path))
		if e != nil {
			t.Fatal(e)
		}
		if text(manifest, path) != fmt.Sprintf("%x", sha256.Sum256(b)) {
			t.Fatalf("source drift %s", path)
		}
	}
	cases := proposalArray(value(root, "cases"))
	if len(cases) != 36 {
		t.Fatalf("case coverage drift %d", len(cases))
	}
	for _, item := range cases {
		v := object(item)
		t.Run(text(v, "name"), func(t *testing.T) {
			calls := []any{[]any{"transaction"}}
			out, err := proposeOuter(object(value(v, "context")), object(value(v, "candidate")), proposalPorts{
				assertion: func(in, c *Object) (assertionproposal.Result, error) {
					calls = append(calls, []any{"assertion", in, c})
					if text(v, "innerStatus") == "rejected" {
						return assertionproposal.Result{Status: "rejected", Reason: "synthetic_inner_rejected"}, nil
					}
					return assertionproposal.Result{Status: "saved", Memory: parseObject(t, `{"id":"backing"}`)}, nil
				},
				checkpoint: func(id string) (*Object, error) {
					calls = append(calls, []any{"checkpoint"}, []any{"state", id})
					return parseObject(t, `{"id":"backing","version":7,"deletionEpoch":3}`), nil
				},
				relations: func(in, ref *Object, p []any) (relations.Admission, error) {
					calls = append(calls, []any{"relations", in, ref, p})
					return relations.Admission{Created: []*Object{}, Rejected: []relations.Rejection{}}, nil
				},
				commitment: func(in, ref *Object, c any) (commitments.CommandResult, error) {
					calls = append(calls, []any{"commitment", in, ref, c})
					if value(v, "commandThrow") == true {
						return commitments.CommandResult{}, errors.New("synthetic_command_write_failure")
					}
					if value(v, "commandReject") == true {
						return commitments.CommandResult{Status: "rejected", Reason: "synthetic_rejected"}, nil
					}
					return commitments.CommandResult{Status: "saved", Item: parseObject(t, `{"ref":{"id":"plan","version":1}}`)}, nil
				},
				knowledge: func(in, ref *Object, p []any) (knowledge.AdmissionResult, error) {
					calls = append(calls, []any{"knowledge", in, ref, p})
					return knowledge.AdmissionResult{Created: []*Object{}, Rejected: []knowledge.AdmissionRejection{}}, nil
				},
			})
			if !deepEqual(calls, value(v, "calls")) {
				got, _ := jsjson.Stringify(calls)
				want, _ := jsjson.Stringify(value(v, "calls"))
				t.Fatalf("calls got %s want %s", got, want)
			}
			if want := text(v, "error"); want != "" {
				if err == nil || err.Error() != want {
					t.Fatalf("error %v want %s", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, e := json.Marshal(out)
			if e != nil {
				t.Fatal(e)
			}
			got, e := jsjson.ParseBytes(encoded)
			if e != nil {
				t.Fatal(e)
			}
			if !deepEqual(got, value(v, "result")) {
				want, _ := jsjson.Stringify(value(v, "result"))
				t.Fatalf("result %s want %s", encoded, want)
			}
		})
	}
}
