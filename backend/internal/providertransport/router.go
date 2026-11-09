package providertransport

import (
	"context"
	"errors"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
)

// RouterFactory captures one current admitted configuration for an operation.
// The returned instance has an independent Policy and immutable routing maps,
// but borrows the installation's same host, credentials and model scheduler.
// It must not return its parent or another factory-backed dispatcher.
type RouterFactory func(context.Context) (*NativeModels, error)
type routerOwners struct {
	mu     sync.Mutex
	work   quiescence.Group
	active map[*NativeModels]struct{}
}

func (m *NativeModels) operationRouter(ctx context.Context) (*NativeModels, func(), error) {
	if m.Router == nil {
		return m, func() {}, nil
	}
	child, err := m.Router(ctx)
	if err != nil {
		return nil, nil, err
	}
	if child == nil || child == m || child.Router != nil || child.Policy == nil || child.Client == nil || child.Host != m.Host {
		return nil, nil, errors.New("native_router_owner_invalid")
	}
	// Caller already owns parent requests admission. Close waits that group before
	// sealing these cleanup tasks, so this Start cannot race past parent closure.
	done, ok := m.routers.work.Start()
	if !ok {
		return nil, nil, safe("native_models_closed")
	}
	m.routers.mu.Lock()
	if m.routers.active == nil {
		m.routers.active = map[*NativeModels]struct{}{}
	}
	m.routers.active[child] = struct{}{}
	m.routers.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			go func() {
				defer done()
				if child.CloseAndWait(context.Background()) == nil {
					m.routers.mu.Lock()
					delete(m.routers.active, child)
					m.routers.mu.Unlock()
				}
			}()
		})
	}
	return child, release, nil
}
func (m *NativeModels) drainRouters(ctx context.Context) error {
	if err := m.routers.work.Wait(ctx); err != nil {
		return err
	}
	// A startup uncertainty can make a background drain return early with error.
	// Such children remain owned here; retry never treats that return as cleanup.
	m.routers.mu.Lock()
	children := make([]*NativeModels, 0, len(m.routers.active))
	for child := range m.routers.active {
		children = append(children, child)
	}
	m.routers.mu.Unlock()
	for _, child := range children {
		if err := child.CloseAndWait(ctx); err != nil {
			return err
		}
		m.routers.mu.Lock()
		delete(m.routers.active, child)
		m.routers.mu.Unlock()
	}
	return nil
}

func (m *NativeModels) HasConfiguredRoute(ctx context.Context, role string) (bool, error) {
	done, ok := m.requests.Start()
	if !ok {
		return false, safe("native_models_closed")
	}
	defer done()
	router, release, err := m.operationRouter(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	if router.Policy == nil {
		return false, safe("native_provider_composition_required")
	}
	return router.Policy.HasConfiguredRoute(role), nil
}

// AcquireRouter retains one source Router snapshot across multi-step operations
// such as vision-route selection and image observation. Release is mandatory.
func (m *NativeModels) AcquireRouter(ctx context.Context) (*NativeModels, func(), error) {
	done, ok := m.requests.Start()
	if !ok {
		return nil, nil, safe("native_models_closed")
	}
	router, release, err := m.operationRouter(ctx)
	if err != nil {
		done()
		return nil, nil, err
	}
	var once sync.Once
	return router, func() { once.Do(func() { release(); done() }) }, nil
}
