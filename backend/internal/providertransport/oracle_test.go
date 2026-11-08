package providertransport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

func TestActualSourceHTTP(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-http.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name, Path, Protocol, Type, Response, RetryAfter string
			Status                                           int
			Body                                             map[string]any
			Output                                           map[string]any
			Received                                         map[string]any
			Error                                            *struct {
				Code         string
				Retryable    bool
				Status       *int
				RetryAfterMS *float64
			}
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			observed := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				row := map[string]any{"method": r.Method, "path": r.URL.Path, "accept": r.Header.Get("Accept"), "authorization": r.Header.Get("Authorization")}
				if value := r.Header.Get("Content-Type"); value != "" {
					row["contentType"] = value
				}
				body, _ := io.ReadAll(r.Body)
				if len(body) > 0 {
					var v any
					if e := json.Unmarshal(body, &v); e != nil {
						t.Error(e)
					}
					row["body"] = v
				}
				observed <- row
				w.Header().Set("Content-Type", c.Type)
				if c.RetryAfter != "" {
					w.Header().Set("Retry-After", c.RetryAfter)
				}
				w.WriteHeader(c.Status)
				_, _ = io.WriteString(w, c.Response)
			}))
			defer server.Close()
			client, provider := nativeTestClient(server)
			if c.Protocol != "" {
				provider.Protocol = c.Protocol
			}
			out, e := client.Request(context.Background(), Request{Provider: provider, Path: c.Path, Body: c.Body})
			if c.Error != nil {
				failure, ok := e.(*modelservice.ModelError)
				if !ok || failure.Code != c.Error.Code || failure.Retryable != c.Error.Retryable || !reflect.DeepEqual(failure.Status, c.Error.Status) || !reflect.DeepEqual(failure.RetryAfterMS, c.Error.RetryAfterMS) {
					t.Fatalf("error %#v != %#v", e, c.Error)
				}
			} else if e != nil || !reflect.DeepEqual(out, c.Output) {
				t.Fatalf("output %#v %v != %#v", out, e, c.Output)
			}
			if got := <-observed; !reflect.DeepEqual(got, c.Received) {
				t.Fatalf("request %#v != %#v", got, c.Received)
			}
		})
	}
}
