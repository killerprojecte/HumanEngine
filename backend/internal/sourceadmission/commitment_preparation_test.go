package sourceadmission

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"testing"
)

// The oracle calls actual outer TS orchestration, intercepting the inner writer
// only after declaration preparation has completed. Native writes are separate.
func TestCommitmentDeclarationActualOuterSource(t *testing.T) {
	raw, err := os.ReadFile("../memoryservice/testdata/outer-proposal-source.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := jsjson.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, v := range arr(get(root, "cases")) {
		input, candidate := object(get(v, "context")), object(get(v, "candidate"))
		if !truthy(get(candidate, "commitment")) || len(arr(get(candidate, "knowledge"))) > 0 {
			continue
		}
		count++
		t.Run(str(get(v, "name")), func(t *testing.T) {
			result, err := PrepareCommitment(input, candidate)
			if err != nil {
				t.Fatal(err)
			}
			var assertion []any
			for _, call := range arr(get(v, "calls")) {
				a := arr(call)
				if len(a) > 0 && a[0] == "assertion" {
					assertion = a
					break
				}
			}
			if assertion == nil {
				if result.OK || result.Reason != get(get(v, "result"), "reason") {
					t.Fatalf("rejection mismatch: %+v", result)
				}
			} else if !result.OK || !deepEqual(result.Context, assertion[1]) || !deepEqual(result.Candidate, assertion[2]) {
				t.Fatalf("declaration mismatch for %s", str(get(v, "name")))
			}
		})
	}
	if count != 28 {
		t.Fatalf("case coverage drift: %d", count)
	}
}
