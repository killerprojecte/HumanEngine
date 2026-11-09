package v1adapterruntime

import (
	"context"
	"errors"
	"testing"
)

func TestStatusOwnerPreservesRawReadAndDisposedIdentity(t *testing.T) {
	reads, cancels := 0, 0
	owner := &adapterStatusOwner{cancel: func() { cancels++ }, read: func(context.Context) (string, error) { reads++; return "connected", nil }}
	if got, err := owner.status(context.Background()); got != "connected" || err != nil || reads != 1 {
		t.Fatal(got, err, reads)
	}
	owner.dispose()
	if got, err := owner.status(context.Background()); got != "disconnected" || err != nil || reads != 1 || cancels != 1 {
		t.Fatal(got, err, reads, cancels)
	}
}
func TestStatusOwnerDoesNotInventHealthOnHostFailure(t *testing.T) {
	failed := errors.New("retired-before-disposal")
	owner := &adapterStatusOwner{cancel: func() {}, read: func(context.Context) (string, error) { return "", failed }}
	if _, err := owner.status(context.Background()); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	owner.read = func(context.Context) (string, error) { owner.dispose(); return "", failed }
	if got, err := owner.status(context.Background()); got != "disconnected" || err != nil {
		t.Fatal(got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := owner.status(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
