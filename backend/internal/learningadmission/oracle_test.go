package learningadmission

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestActualSourceAdmissionTrace(t *testing.T) {
	raw, e := os.ReadFile("../semantic/testdata/runtime-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Trace []struct {
			Op     string
			Args   []any
			Result any
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	a, now := setup(t)
	for _, step := range fixture.Trace {
		var got any
		str := func(i int) string { return step.Args[i].(string) }
		num := func(i int) int64 { return int64(step.Args[i].(float64)) }
		switch step.Op {
		case "now":
			*now = num(0)
		case "admit":
			got, e = a.Admit(str(0), str(1), str(2))
		case "inspect":
			s := a.Inspect(str(0), Capacity{})
			got = map[string]any{"state": s.State}
			if s.State == "waiting" || s.State == "ready" {
				got = map[string]any{"state": s.State, "dueAt": s.DueAt, "firstAdmittedAt": s.FirstAdmittedAt, "pending": s.Pending, "lane": s.Lane}
			}
		case "take":
			var b *Batch
			b, e = a.Take(str(0), int(num(1)), Capacity{})
			got = batchJSON(b)
		case "finish":
			retry := num(2)
			got, e = a.Finish(str(0), uint64(num(1)), &retry)
		case "recall":
			got = batchJSON(a.Recall(str(0), str(1), step.Args[2].(bool)))
		case "cancelScene":
			got = batchJSON(a.CancelScene(str(0)))
		case "setEnabled":
			batches := a.SetEnabled(step.Args[0].(bool))
			values := []any{}
			for i := range batches {
				values = append(values, batchJSON(&batches[i]))
			}
			got = values
		default:
			t.Fatal(step.Op)
		}
		if e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal(got)
		if e != nil {
			t.Fatal(e)
		}
		var canonical any
		if e = json.Unmarshal(encoded, &canonical); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(canonical, step.Result) {
			t.Fatalf("%s %v: got %#v want %#v", step.Op, step.Args, canonical, step.Result)
		}
	}
}
func batchJSON(b *Batch) any {
	if b == nil {
		return nil
	}
	return map[string]any{"sceneId": b.SceneID, "token": b.Token, "lane": b.Lane, "sourceKeys": b.SourceKeys}
}
