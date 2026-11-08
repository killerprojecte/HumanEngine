package providertransport

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

type readGuardKey struct{}

// WithModelReadGuard carries the actual Engine ticket validation to each
// provider attempt. It neither replaces budget policy nor caches authorization.
func WithModelReadGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	previous, _ := ctx.Value(readGuardKey{}).(func(context.Context) error)
	return context.WithValue(ctx, readGuardKey{}, func(c context.Context) error {
		if previous != nil {
			if e := previous(c); e != nil {
				return e
			}
		}
		if guard != nil {
			return guard(c)
		}
		return nil
	})
}
func checkModelRead(ctx context.Context) error {
	if guard, _ := ctx.Value(readGuardKey{}).(func(context.Context) error); guard != nil {
		e := guard(ctx)
		if e != nil && modelservice.IsReadAdmissionCode(e.Error()) {
			return modelservice.NewError(e.Error(), nil, false)
		}
		return e
	}
	return nil
}
