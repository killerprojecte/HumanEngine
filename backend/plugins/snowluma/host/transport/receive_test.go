package snowlumatransport

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func verifyReceiveOracle(t *testing.T, host *plugins.WASMHost) {
	t.Helper()
	raw, err := os.ReadFile("../../core/testdata/receive-source.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsjson.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	call, err := host.BindCall("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range parsed.([]any) {
		row := v.(*jsjson.Object)
		name, _ := row.Get("name")
		t.Run("receive-"+name.(string), func(t *testing.T) {
			input, _ := row.Get("input")
			wire, err := jsjson.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			out, err := call(context.Background(), wire)
			if err != nil {
				t.Fatal(err)
			}
			value, err := jsjson.ParseBytes(out)
			if err != nil {
				t.Fatal(err)
			}
			decision := value.(*jsjson.Object)
			get := func(key string) any { v, _ := decision.Get(key); return normalizationValue(v) }
			trace, remembered := []any{}, []any{}
			if id, ok := decision.Get("rememberRecall"); ok {
				remembered = append(remembered, id)
			}
			kind, _ := decision.Get("kind")
			switch kind {
			case "response":
				trace = append(trace, map[string]any{"kind": "cleanup"})
				result := map[string]any{"kind": "response"}
				if v, ok := decision.Get("data"); ok {
					result["data"] = normalizationValue(v)
				}
				trace = append(trace, result)
			case "response_error":
				trace = append(trace, map[string]any{"kind": "cleanup"}, map[string]any{"kind": "response_error", "code": get("error"), "transmitted": false})
			case "message":
				trace = append(trace, map[string]any{"kind": "message", "message": get("message"), "scene": get("scene")})
			case "recall":
				trace = append(trace, map[string]any{"kind": "recall", "recall": get("recall"), "scene": get("scene")})
			case "error":
				trace = append(trace, map[string]any{"kind": "error", "code": get("error")})
			case "ignore", "remember_recall":
			default:
				t.Fatalf("unknown guest decision %s", out)
			}
			if strings.HasSuffix(name.(string), "callback-throws") {
				trace = append(trace, map[string]any{"kind": "error", "code": "invalid_event_or_callback_failed"})
			}
			sourceTrace, _ := row.Get("trace")
			sourceRemembered, _ := row.Get("remembered")
			if !reflect.DeepEqual(trace, normalizationValue(sourceTrace)) || !reflect.DeepEqual(remembered, normalizationValue(sourceRemembered)) {
				t.Fatalf("guest %s trace %#v remembered %#v; actual source %s", out, trace, remembered, raw)
			}
		})
	}
}
