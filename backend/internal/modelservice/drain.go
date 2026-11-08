package modelservice

import "context"

// Drain seals new policy requests and waits for actual policy runners, including
// a runner that lost the role-timeout race and still owns unpublished output.
// Native invocation owners must additionally drain physical attempts: a custom
// protocol cancellation may settle policy before the provider settles.
func (p *Policy) Drain(ctx context.Context) error { return p.work.Wait(ctx) }
