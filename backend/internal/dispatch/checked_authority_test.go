package dispatch

import (
	"errors"
	"fmt"
	"testing"
)

func TestCheckedAuthorityPreservesFailureBoundary(t *testing.T) {
	for _, port := range []string{"eligible", "recalled"} {
		for at := 1; at <= 4; at++ {
			t.Run(fmt.Sprintf("%s_%d", port, at), func(t *testing.T) {
				cause := errors.New("synthetic_source_read_failed")
				calls, invoked, effects := 0, 0, 0
				adapter := &Adapter{Send: func(_ SceneRef, _ Segment, _ *Signal, gate func() error) *Future[Receipt] {
					invoked++
					if err := gate(); err != nil {
						return Rejected[Receipt](err)
					}
					effects++
					return Resolved(Receipt{Status: Sent})
				}}
				services, turn, guard := testPorts(NewSignal(), adapter, nil)
				services.Eligible = func(*TurnContext) bool { t.Fatal("legacy eligible called"); return false }
				services.IsRecalled = func(string, string) bool { t.Fatal("legacy recalled called"); return false }
				services.EligibleChecked = func(*TurnContext) (bool, error) {
					if port == "eligible" {
						calls++
						if calls == at {
							return false, cause
						}
					}
					return true, nil
				}
				services.IsRecalledChecked = func(string, string) (bool, error) {
					if port == "recalled" {
						calls++
						if calls == at {
							return false, cause
						}
					}
					return false, nil
				}
				receipt, failure := DispatchSegment(services, turn, Segment{ReplyToID: text("source")}, "operation", guard, nil, 0, 1)
				if calls != at || effects != 0 {
					t.Fatal(calls, effects)
				}
				if at < 4 {
					if invoked != 0 || !errors.Is(failure, cause) || receipt.Status != "" {
						t.Fatal(invoked, receipt, failure)
					}
				} else if invoked != 1 || failure != nil || receipt.Status != Unknown || !errors.Is(OutcomeError(receipt), cause) {
					t.Fatal(invoked, receipt, failure, OutcomeError(receipt))
				}
			})
		}
	}
}

func TestCheckedAuthorityNeverRechecksAuthoritativeReceipt(t *testing.T) {
	for _, status := range []Status{Sent, Unknown} {
		t.Run(string(status), func(t *testing.T) {
			effectFinished := false
			adapter := &Adapter{Send: func(_ SceneRef, _ Segment, _ *Signal, gate func() error) *Future[Receipt] {
				if err := gate(); err != nil {
					return Rejected[Receipt](err)
				}
				effectFinished = true
				return Resolved(Receipt{Status: status})
			}}
			services, turn, guard := testPorts(NewSignal(), adapter, nil)
			checks := 0
			services.EligibleChecked = func(*TurnContext) (bool, error) {
				checks++
				if effectFinished {
					return false, errors.New("late_authority_failure")
				}
				return true, nil
			}
			receipt, failure := DispatchSegment(services, turn, Segment{}, "operation", guard, nil, 0, 1)
			if failure != nil || receipt.Status != status || checks != 4 {
				t.Fatal(receipt, failure, checks)
			}
		})
	}
}
