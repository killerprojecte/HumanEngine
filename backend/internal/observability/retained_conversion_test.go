package observability

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestRetainedConversionsActualJS(t *testing.T) {
	raw, err := os.ReadFile("retained-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Conversions []struct {
			Name   string
			Truthy bool
			Text   string
		}
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{
		"undefined": jsjson.Undefined, "null": nil, "empty": "", "text": "中文🙂", "true": true, "false": false,
		"zero": float64(0), "negative-zero": math.Copysign(0, -1), "finite": 1.25, "nan": math.NaN(),
		"positive-infinity": math.Inf(1), "negative-infinity": math.Inf(-1), "object": new(jsjson.Object),
		"array": []any{nil, jsjson.Undefined, "x", false}, "native-function": retainedNativeFunction("Object"),
	}
	if len(source.Conversions) != len(values) {
		t.Fatal("incomplete conversion coverage")
	}
	for _, c := range source.Conversions {
		t.Run(c.Name, func(t *testing.T) {
			value, ok := values[c.Name]
			if !ok {
				t.Fatal("unknown actual source type")
			}
			if got := retainedTruthy(value); got != c.Truthy {
				t.Fatalf("Boolean = %v source %v", got, c.Truthy)
			}
			if got := retainedText(value); got != c.Text {
				t.Fatalf("String = %q source %q", got, c.Text)
			}
		})
	}
}
