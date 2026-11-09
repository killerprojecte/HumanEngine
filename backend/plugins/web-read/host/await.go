//go:build web_read_candidate

package host

import "context"

// A deadline cancels admission, not already-running synchronous source policy.
// The admitted binder owns the exact queue/execute transition. Physical work
// remains leased until that synchronous call (including free) really finishes.
func admittedGuest(wait, life context.Context, begin func() (func(), bool), call func(context.Context, context.Context, []byte) ([]byte, error), input []byte) ([]byte, error) {
	done, ok := begin()
	if !ok {
		return nil, context.Canceled
	}
	defer done()
	return call(wait, life, input)
}
