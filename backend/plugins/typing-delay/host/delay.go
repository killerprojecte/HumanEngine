// Package host composes the typing-delay guest with native timer facilities.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"math"
	"time"
)

// Ports borrows the manager lifetime and physical work admission. It owns no
// additional work registry and never substitutes turn cancellation for guest life.
type Ports struct {
	Life      context.Context
	Config    json.RawMessage
	BeginWork func() (func(), bool)
	Current   func() bool
	Lifetime  func(context.Context, context.Context) (context.Context, func())
	CallGuest func(context.Context, []byte) ([]byte, error)
}

func Delay(ports Ports) dispatch.TypingDelay {
	return func(segment dispatch.Segment, turn *dispatch.Signal) *dispatch.Future[struct{}] {
		result := dispatch.NewFuture[struct{}]()
		done, ok := ports.BeginWork()
		if !ok {
			result.Reject(context.Canceled)
			return result
		}
		go func() {
			defer done()
			combined, cancel := ports.Lifetime(turn, ports.Life)
			defer cancel()
			if combined.Err() != nil || !ports.Current() {
				result.Reject(context.Canceled)
				return
			}
			input, err := json.Marshal(map[string]any{"operation": "delay", "segment": segment, "config": ports.Config})
			if err != nil {
				result.Reject(err)
				return
			}
			// Guest policy is synchronous source work. Turn cancellation must not destroy
			// a shared guest; plugin lifetime still interrupts and physically unloads it.
			output, err := ports.CallGuest(ports.Life, input)
			if err != nil {
				result.Reject(err)
				return
			}
			var policy struct {
				DelayMS float64 `json:"delayMs"`
				Error   string  `json:"error"`
			}
			if err = json.Unmarshal(output, &policy); err != nil || policy.Error != "" || math.IsNaN(policy.DelayMS) || math.IsInf(policy.DelayMS, 0) || policy.DelayMS < 0 {
				result.Reject(errors.New("invalid_typing_delay_policy"))
				return
			}
			timer := time.NewTimer(time.Duration(policy.DelayMS) * time.Millisecond)
			defer timer.Stop()
			select {
			case <-combined.Done():
				result.Reject(combined.Err())
			case <-timer.C:
				if !ports.Current() || combined.Err() != nil {
					result.Reject(context.Canceled)
				} else {
					result.Resolve(struct{}{})
				}
			}
		}()
		return result
	}
}
