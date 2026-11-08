package providertransport

import (
	"context"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

type ModelCallSink interface {
	Record(context.Context, modelcalls.Observation) error
}

func (m *NativeModels) withCallObserver(ctx context.Context) context.Context {
	if m.Calls == nil {
		return ctx
	}
	sink := m.Calls
	return modelservice.WithAttemptObserver(ctx, func(ctx context.Context, event modelservice.AttemptObservation) {
		entry := modelcalls.Observation{At: event.At.UTC().Format("2006-01-02T15:04:05.000Z"), ModelID: event.ModelID, ProviderID: event.ProviderID, APIModel: event.APIModelName, Role: event.Role, Attempt: event.Number, DurationMS: float64(event.Duration) / float64(time.Millisecond), TransportMode: event.TransportMode, FirstTokenMS: event.FirstTokenMS, FirstTokenKind: event.FirstTokenKind, Event: "model.failed", FailureReason: event.FailureReason}
		if event.Completed {
			entry.Event = "model.completed"
			if u := event.Usage; u != nil {
				entry.Usage = &modelcalls.Usage{Known: u.Known, Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheWriteInputTokens}
			}
		}
		// The synchronous SQL owner remains within Policy drain even after cancellation.
		_ = sink.Record(context.WithoutCancel(ctx), entry)
	})
}
func observeCallUsage(ctx context.Context, usage map[string]any) {
	if usage == nil || !modelservice.AttemptObservationActive(ctx) {
		return
	}
	number := func(key string) *float64 {
		value, ok := usage[key].(float64)
		if !ok {
			return nil
		}
		return &value
	}
	modelservice.ObserveAttemptUsage(ctx, &modelservice.AttemptUsage{Known: usage["known"] != false, InputTokens: number("inputTokens"), OutputTokens: number("outputTokens"), CacheReadInputTokens: number("cacheReadInputTokens"), CacheWriteInputTokens: number("cacheWriteInputTokens")})
}
