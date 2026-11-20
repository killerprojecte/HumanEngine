package providertransport

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

type readGuardKey struct{}
type finalReadGuardKey struct{}

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

// WithFinalModelReadGuard also rechecks this exact per-source callback at the
// native send boundary. Ordinary guarded prompts do not acquire a second read.
func WithFinalModelReadGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(WithModelReadGuard(ctx, guard), finalReadGuardKey{}, guard)
}

func checkModelRead(ctx context.Context) error {
	guard, _ := ctx.Value(readGuardKey{}).(func(context.Context) error)
	return runModelReadGuard(ctx, guard)
}

func finalModelReadGuard(ctx context.Context) func(context.Context) error {
	guard, _ := ctx.Value(finalReadGuardKey{}).(func(context.Context) error)
	return guard
}

func checkFinalModelRead(ctx context.Context, guard func(context.Context) error) error {
	if err := runModelReadGuard(ctx, guard); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return safe("request_cancelled")
	}
	return nil
}

func runModelReadGuard(ctx context.Context, guard func(context.Context) error) error {
	if guard == nil {
		return nil
	}
	e := guard(ctx)
	if e != nil && modelservice.IsReadAdmissionCode(e.Error()) {
		return modelservice.NewError(e.Error(), nil, false)
	}
	return e
}
