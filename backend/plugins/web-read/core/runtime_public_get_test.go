//go:build web_read_public_get && !web_read_candidate

package webread

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicGetPreviewAndCacheReference(t *testing.T) {
	raw, err := json.Marshal(Invoke([]byte(`{"operation":"publicGetRegistrations"}`)))
	if err != nil {
		t.Fatal(err)
	}
	var definitions []struct{ Name, Effect string }
	if err := json.Unmarshal(raw, &definitions); err != nil || len(definitions) != 1 ||
		definitions[0].Name != "web-read.read" || definitions[0].Effect != "read" {
		t.Fatal(string(raw), err)
	}
	html := "<title>Public manual</title><h1>Visible heading</h1><script>not visible</script><p>" + strings.Repeat("filler ", 80)
	in := map[string]any{
		"operation": "publicGetResponse", "httpStatus": 200, "sourceUrl": "https://public.example/",
		"contentEncoding": "identity", "config": map[string]any{"maxChars": 256},
		"body": map[string]any{"contentType": "text/html; charset=utf-8", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(html)),
			"resultId": "original", "truncated": true, "nextOffset": 8192},
	}
	input, _ := json.Marshal(in)
	output := Invoke(input).(map[string]any)
	body := output["body"].(map[string]any)
	if output["title"] != "Public manual" || len([]rune(output["text"].(string))) != 256 ||
		!strings.Contains(output["text"].(string), "Visible heading") || strings.Contains(output["text"].(string), "not visible") ||
		output["truncated"] != true || body["resultId"] != "original" || body["content"] != nil ||
		body["nextOffset"] != float64(8192) {
		t.Fatal(output)
	}
	for _, operation := range []string{"registrations", "networkPrepare", "networkChoose", "plan"} {
		input, _ := json.Marshal(map[string]string{"operation": operation})
		if Invoke(input).(map[string]any)["error"] != "unknown_plugin_operation" {
			t.Fatal("request or send operation leaked:", operation)
		}
	}
}
