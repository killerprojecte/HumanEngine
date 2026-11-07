package sourceevents

import "errors"

// BorrowReadScope composes source-event reads with another domain's already
// acquired read/write capability. Both functions MUST check that same live
// scope. It never opens a connection, acquires an authority lock, or enrolls a
// schema. The view does not cache schema presence because its owner may write.
func BorrowReadScope(ready func() error, query func(string, ...any) (Rows, error)) (*View, error) {
	if ready == nil || query == nil {
		return nil, errors.New("source_event_read_scope_required")
	}
	return &View{ready: ready, query: query}, nil
}
