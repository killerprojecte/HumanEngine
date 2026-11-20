package app

import "context"

// Index work is a child of the accepted request, not of its connection.
// Application shutdown joins it before releasing the shared learning/model/DB owners.
func (a *Application) queueManualMemoryIndex(ctx context.Context, id, content string) {
	finish, _ := a.requests.Child()
	go func() {
		defer finish()
		a.Learning.Owner.IndexManualMemory(context.WithoutCancel(ctx), id, content)
	}()
}
