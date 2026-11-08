package providertransport

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"testing"
)

func TestNativeDrainRetainsUncooperativePhysicalAttemptAndCallback(t *testing.T) {
	m := new(NativeModels)
	started, release, callback, releaseCallback, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	invoke := m.asyncInvocation(func(_ context.Context, _ modelservice.Attempt, settle func(any, error)) {
		close(started)
		<-release
		settle(nil, nil)
	})
	invoke(context.Background(), modelservice.Attempt{}, func(any, error) { close(callback); <-releaseCallback; close(finished) })
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := m.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("released physical attempt: %v", e)
	}
	close(release)
	<-callback
	if e := m.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("released late cleanup callback: %v", e)
	}
	close(releaseCallback)
	<-finished
	if e := m.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	denied := false
	invoke(context.Background(), modelservice.Attempt{}, func(_ any, e error) { denied = e != nil })
	if !denied {
		t.Fatal("new attempt after seal")
	}
}
