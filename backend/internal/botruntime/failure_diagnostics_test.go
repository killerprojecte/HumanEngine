package botruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"strings"
	"testing"
)

func TestRuntimeFailureDiagnosticsDoNotExposeCause(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{nil, ""}, {context.Canceled, "request_cancelled"}, {context.DeadlineExceeded, "deadline_exceeded"},
		{fmt.Errorf("synthetic wrapper: %w", jsjson.ErrUnsupportedType), "json_value_type_unsupported"},
		{errors.New("memory_read_revoked"), "memory_read_revoked"},
		{errors.New("provider secret=synthetic-sensitive-body"), "runtime_operation_failed"},
		{errors.New("memory_read_revoked synthetic-sensitive-body"), "runtime_operation_failed"},
	} {
		if got := runtimeFailureCode(test.err); got != test.code {
			t.Errorf("classification %q != %q", got, test.code)
		}
	}
	cause := errors.New("synthetic-sensitive-body")
	encoded, err := json.Marshal(Event{Stage: "planner", Reason: "turn_failed", FailureCode: runtimeFailureCode(cause), Cause: cause})
	if err != nil || strings.Contains(string(encoded), "synthetic-sensitive-body") || strings.Contains(string(encoded), "Cause") {
		t.Fatalf("cause leaked in JSON: %s %v", encoded, err)
	}
}
