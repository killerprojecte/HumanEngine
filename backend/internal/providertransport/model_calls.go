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

type ModelUsageSink interface {
	Record(context.Context, modelservice.AttemptObservation, map[string]float64) error
}

func (m *NativeModels) withCallObserver(ctx context.Context) context.Context {
	if usage := m.Usage; usage != nil {
		ctx = modelservice.WithUsageObserver(ctx, func(ctx context.Context, event modelservice.AttemptObservation) {
			_ = usage.Record(context.WithoutCancel(ctx), event, m.Prices[event.ModelID])
		})
	}
	sink := m.Calls
	if sink == nil {
		return ctx
	}
	return modelservice.WithAttemptObserver(ctx, func(ctx context.Context, event modelservice.AttemptObservation) {
		entry := modelcalls.Observation{At: event.At.UTC().Format("2006-01-02T15:04:05.000Z"), ModelID: event.ModelID, ProviderID: event.ProviderID, APIModel: event.APIModelName, Role: event.Role, Attempt: event.Number, DurationMS: float64(event.Duration) / float64(time.Millisecond), TransportMode: event.TransportMode, FirstTokenMS: event.FirstTokenMS, FirstTokenKind: event.FirstTokenKind, Event: "model.failed", FailureReason: event.FailureReason}
		if event.Completed {
			entry.Event = "model.completed"
			if u := event.Usage; u != nil {
				entry.Usage = &modelcalls.Usage{Known: u.Known, Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheWriteInputTokens}
			}
		}
		// Both synchronous sinks finish within the physical Policy drain.
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
	modelservice.ObserveAttemptUsage(ctx, &modelservice.AttemptUsage{
		Known: usage["known"] != false, CacheKnown: usage["cacheKnown"] == true,
		InputTokens: number("inputTokens"), OutputTokens: number("outputTokens"),
		OrdinaryInputTokens: number("ordinaryInputTokens"), CacheReadInputTokens: number("cacheReadInputTokens"),
		CacheWriteInputTokens: number("cacheWriteInputTokens"),
		CacheWriteTTL: [4]*float64{number("cacheWrite5mInputTokens"), number("cacheWrite1hInputTokens"),
			number("cacheWrite30mInputTokens"), number("cacheWrite12hInputTokens")},
	})
}
