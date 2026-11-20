package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/learningowner"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestEmbeddingActionRawActualSource(t *testing.T) {
	raw, err := os.ReadFile("../app/testdata/embedding-action-boundary-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Node          string
		SourceHashes  map[string]string
		ProviderCalls int
		Cases         []struct {
			Name, Method, Path, Type, ResponseBody string
			RequestBody                            *string
			Auth, HasServerRequestID               bool
			Status                                 int
			Headers                                map[string]string
			Calls                                  [][]any
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" || len(fixture.Cases) != 54 || fixture.ProviderCalls != 0 {
		t.Fatalf("fixture: %+v %v", fixture.Node, err)
	}
	for path, want := range fixture.SourceHashes {
		raw, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
			t.Fatalf("source drift %s %v", path, err)
		}
	}
	s, db, a, _, _ := configurationFixture(t)
	p, err := recovery.NewReadPublisher(t.Context(), db, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.CloseAndWait(context.Background()); err != nil {
			t.Error(err)
		}
	})
	s.embeddingActionPublisher = p
	s.token = "synthetic-token"
	trace := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			calls := [][]any{}
			var failStatus bool
			s.embeddingIndexAction = func(_ context.Context, input learningowner.IndexAction) error {
				var id any
				if input.ExpectedJobID != nil {
					id = *input.ExpectedJobID
				}
				switch input.Action {
				case "start":
					calls = append(calls, []any{"start", id, input.ExpectedEncoderRevision})
				case "resume":
					calls = append(calls, []any{"resume", input.ConfirmUnknownRequest, id})
				default:
					calls = append(calls, []any{input.Action, id})
				}
				failStatus = id == "@status-fail"
				if id == "@fail" {
					return errors.New("PRIVATE_PROVIDER_FAILURE")
				}
				return nil
			}
			s.embeddingIndexRead = func(_ context.Context, emit func([]byte) error) (bool, error) {
				calls = append(calls, []any{"status"})
				if failStatus {
					return true, errors.New("PRIVATE_STATUS_FAILURE")
				}
				return true, emit([]byte(`{"state":"unconfigured","coverage":{"eligible":0,"indexed":0,"pending":0},"retained":{"generations":0,"vectors":0}}`))
			}
			var input string
			if c.RequestBody != nil {
				input = *c.RequestBody
			}
			req := httptest.NewRequest(c.Method, c.Path, strings.NewReader(input))
			if c.Type != "" {
				req.Header.Set("Content-Type", c.Type)
			}
			req.Header.Set("Authorization", "Bearer wrong-token")
			if c.Auth {
				req.Header.Set("Authorization", "Bearer synthetic-token")
			}
			req.Header.Set("X-Request-ID", "synthetic-spoof")
			out := httptest.NewRecorder()
			s.ServeHTTP(out, req)
			if out.Code != c.Status {
				t.Fatalf("status %d want %d, body %s", out.Code, c.Status, out.Body)
			}
			for key, want := range c.Headers {
				if got := out.Header().Get(key); got != want {
					t.Fatalf("%s: got %q want %q", key, got, want)
				}
			}
			id := out.Header().Get("X-Request-ID")
			if trace.MatchString(id) != c.HasServerRequestID {
				t.Fatalf("trace %q", id)
			}
			got := strings.Replace(out.Body.String(), `"requestId":"`+id+`"`, `"requestId":"<request-id>"`, 1)
			if got != c.ResponseBody || !reflect.DeepEqual(calls, c.Calls) {
				t.Fatalf("body %s (%s), calls %v (%v)", got, c.ResponseBody, calls, c.Calls)
			}
		})
	}
}
