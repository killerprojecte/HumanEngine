package quiescence

import "context"

type contextGroupKey struct{}

// WithGroup carries a borrowed operation lifetime through scheduler and policy
// contexts. The caller must hold a root until its synchronous submission ends.
func WithGroup(ctx context.Context, g *Group) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextGroupKey{}, g)
}

// ContextChild tracks work accepted from an already-counted caller. Absence of
// a caller scope leaves existing service lifetime behavior unchanged.
func ContextChild(ctx context.Context) (func(), bool) {
	if ctx != nil {
		if g, ok := ctx.Value(contextGroupKey{}).(*Group); ok && g != nil {
			return g.Child()
		}
	}
	return func() {}, true
}
