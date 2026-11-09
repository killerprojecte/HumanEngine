package app

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
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
