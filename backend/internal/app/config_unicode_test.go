package app

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestConfiguredLoneSurrogateReachesNativeProviderUnchanged(t *testing.T) {
	raw, e := os.ReadFile("../contract/testdata/http-strings-node.json")
	if e != nil {
		t.Fatal(e)
	}
	var oracle struct {
		Fixtures []struct {
			Label, Input string
			Valid        bool
		}
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	input := ""
	for _, f := range oracle.Fixtures {
		if f.Label == "models.0.apiModelName-lone-length-1" && f.Valid {
			input = f.Input
			break
		}
	}
	if input == "" {
		t.Fatal("missing actual source string oracle")
	}
	received := make(chan any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, e := io.ReadAll(r.Body)
		if e != nil {
			received <- e
			return
		}
		v, e := jsjson.ParseBytes(b)
		if e != nil {
			received <- e
			return
		}
		received <- field(v, "model")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()
	options := applicationOptions(t)
	options.Entries = []string{}
	app, e := Open(context.Background(), options)
	if e != nil {
		t.Fatal(e)
	}
	defer app.Shutdown(context.Background())
	input = strings.ReplaceAll(input, "https://example.com/v1", server.URL)
	input = strings.ReplaceAll(input, `"thinker"`, `"replyer"`)
	object, e := jsjson.Parse(input)
	if e != nil {
		t.Fatal(e)
	}
	expected := field(field(object, "models").([]any)[0], "apiModelName").(string)
	credentials, _ := jsjson.Parse(`{"provider":{"action":"set","value":"synthetic-only"}}`)
	object.(*jsjson.Object).Set("credentials", credentials)
	b, e := jsjson.Marshal(object)
	if e != nil {
		t.Fatal(e)
	}
	appRequest(t, app, http.MethodPut, "/api/models", json.RawMessage(b))
	completed, e := app.models.Complete(context.Background(), providertransport.CompletionRequest{Role: "replyer", Messages: []*providertransport.Message{{Visible: map[string]any{"role": "user", "content": "synthetic"}}}})
	if e != nil {
		t.Fatal(e)
	}
	defer completed.Release()
	got := <-received
	s, ok := got.(string)
	if !ok {
		t.Fatal(got)
	}
	wantUnits, e := jsjson.UTF16(expected)
	if e != nil {
		t.Fatal(e)
	}
	gotUnits, e := jsjson.UTF16(s)
	if e != nil || !reflect.DeepEqual(gotUnits, wantUnits) {
		t.Fatalf("native model UTF16=%v want%v error=%v", gotUnits, wantUnits, e)
	}
}
