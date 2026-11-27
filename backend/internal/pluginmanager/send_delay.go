package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
)

type removeSendDelay func()

func (remove removeSendDelay) Dispose()                    { remove() }
func (removeSendDelay) CloseAndWait(context.Context) error { return nil }

// StartSendDelay binds a V2 send-delay guest to the complete-segment dispatch
// hook. Timing policy stays in the guest; timer work belongs to the manager.
func (m *Manager) StartSendDelay(_ context.Context, in BuiltinRuntimeInput) (BuiltinLifetime, error) {
	remove, err := m.runtime.InstallTypingDelay(in.Life, func(segment dispatch.Segment, turn *dispatch.Signal) *dispatch.Future[struct{}] {
		result := dispatch.NewFuture[struct{}]()
		done, ok := in.BeginWork()
		if !ok {
			result.Reject(context.Canceled)
			return result
		}
		go func() {
			defer done()
			if err := sendDelay(in, segment, turn); err != nil {
				result.Reject(err)
			} else {
				result.Resolve(struct{}{})
			}
		}()
		return result
	})
	if err != nil {
		return nil, err
	}
	return removeSendDelay(remove), nil
}

func sendDelay(in BuiltinRuntimeInput, segment dispatch.Segment, turn *dispatch.Signal) error {
	combined, cancel := in.JoinLife(turn, in.Life)
	defer cancel()
	if err := combined.Err(); err != nil {
		return err
	}
	if !in.Current() {
		return context.Canceled
	}
	input, err := json.Marshal(struct {
		Operation string           `json:"operation"`
		Segment   dispatch.Segment `json:"segment"`
		Config    json.RawMessage  `json:"config"`
	}{"delay", segment, in.Config})
	if err != nil {
		return err
	}
	// Cancel the turn's wait without terminating a guest shared by other turns.
	output, err := in.CallGuest(in.Life, input)
	if err != nil {
		return err
	}
	var policy struct {
		DelayMS *int64 `json:"delayMs"`
		Error   string `json:"error"`
	}
	if err = json.Unmarshal(output, &policy); err != nil || policy.Error != "" ||
		policy.DelayMS == nil || *policy.DelayMS < 0 || *policy.DelayMS > math.MaxInt64/int64(time.Millisecond) {
		return errors.New("invalid_send_delay_policy")
	}
	if *policy.DelayMS > 0 {
		timer := time.NewTimer(time.Duration(*policy.DelayMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-combined.Done():
			return combined.Err()
		case <-timer.C:
		}
	}
	if !in.Current() {
		return context.Canceled
	}
	return combined.Err()
}
