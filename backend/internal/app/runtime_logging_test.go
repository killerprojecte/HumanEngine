package app

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
	"strings"
	"testing"
)

func TestRuntimeLoggingKeepsSafeFailureCodeAndStage(t *testing.T) {
	output := new(applicationLogBuffer)
	logs := observability.NewStoreLogger(observability.StoreLoggerOptions{Database: ":memory:", Registry: logdelivery.NewRegistry(output)})
	a := &Application{options: Options{Logs: logs}}
	a.observeRuntime(botruntime.Event{SceneID: "scene", TurnID: "turn", Stage: "planner", Status: "failed", Reason: "stage_failed", FailureCode: "json_value_type_unsupported", Cause: errors.New("synthetic-sensitive-body")})
	if err := logs.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	actual := output.snapshot()
	for _, field := range []string{`"failureCode":"json_value_type_unsupported"`, `"stage":"planner"`, `"sceneId":"scene"`, `"turnId":"turn"`} {
		if !strings.Contains(actual, field) {
			t.Fatalf("missing diagnostic field %s: %s", field, actual)
		}
	}
	if strings.Contains(actual, "synthetic-sensitive-body") {
		t.Fatal("raw cause leaked")
	}
}

func TestModelLoggingAddsOnlyTheOptionalSafeStreamCause(t *testing.T) {
	output := new(applicationLogBuffer)
	logs := observability.NewStoreLogger(observability.StoreLoggerOptions{Database: ":memory:", Registry: logdelivery.NewRegistry(output)})
	a := &Application{options: Options{Logs: logs}}
	a.observeModel(modelservice.Event{Kind: "failed", ModelID: "stream", Code: "provider_stream_invalid", StreamCauseCode: "provider_stream_invalid_json"})
	a.observeModel(modelservice.Event{Kind: "failed", ModelID: "ordinary", Code: "provider_http_500"})
	if err := logs.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := output.snapshot()
	if strings.Count(got, `"code":"model.failed"`) != 2 || strings.Count(got, `"causeCode":`) != 1 ||
		!strings.Contains(got, `"causeCode":"provider_stream_invalid_json"`) || !strings.Contains(got, `"reason":"provider_http_500"`) {
		t.Fatalf("model failure logs: %s", got)
	}
}
