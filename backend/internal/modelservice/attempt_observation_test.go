package modelservice

import (
	"context"
	"errors"
	"testing"
)

func TestAttemptFailureSourcePriority(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	stage, stop := context.WithCancelCause(context.Background())
	stop(errors.New("model_stage_timeout"))
	first := 1.0
	for _, tc := range []struct {
		name, mode, code string
		ctx              context.Context
		first            *float64
		want             string
	}{
		{"idle beats cancel", "stream", "provider_stream_idle_timeout", canceled, &first, "stream_idle_timeout"},
		{"timeout beats cancel", "stream", "provider_timeout", canceled, nil, "first_content_timeout"},
		{"stage nonstream", "non_stream", "", stage, nil, "request_timeout"},
		{"cancel beats interrupted", "stream", "provider_stream_interrupted", canceled, &first, "cancelled"},
		{"paid validation", "stream", "validation_error", context.Background(), &first, "stream_interrupted"},
		{"ordinary failure", "custom", "validation_error", context.Background(), nil, "request_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := attemptFailureReason(tc.ctx, tc.mode, tc.first, &ModelError{Code: tc.code}); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestAttemptUsageMetadataCopiesAndPreservesUnknown(t *testing.T) {
	state := &attemptState{}
	ctx := context.WithValue(context.Background(), attemptStateKey{}, state)
	number := 2.0
	input := &AttemptUsage{Known: false, InputTokens: &number}
	ObserveAttemptUsage(ctx, input)
	number = 99
	if state.usage.Known || *state.usage.InputTokens != 2 || state.usage.OutputTokens != nil {
		t.Fatal("unknown/missing usage changed", state.usage)
	}
	state.closed = true
	ObserveAttemptUsage(ctx, &AttemptUsage{Known: true})
	if state.usage.Known || AttemptObservationActive(ctx) {
		t.Fatal("late observation changed sealed attempt")
	}
}
