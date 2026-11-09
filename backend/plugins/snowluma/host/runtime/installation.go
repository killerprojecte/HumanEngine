package snowlumaruntime

import (
	"context"
	"errors"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	transport "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/transport"
)

// InstallationPorts borrow one application's owners. Registry is created once
// for the application, never per generation. Receive must already drive guest
// receive/normalization decisions into that application's real source owners.
type InstallationPorts struct {
	Registry     *Registry
	Sender       Ports
	BuildReceive func(*Sender) (func(context.Context, string) error, error)
	Stopped      func() error
	Status       func(string)
	Error        func(string, error)
}
type Installation struct {
	sender       *Sender
	registration *Registration
	stopped      func() error
	stoppedErr   error
	once         sync.Once
	detach       func() bool
}

// InstallPrepared accepts the configuration/startup plan produced by the bound
// guest. It performs only the requested transport IO and source registration
// order. The application binds it through Manager with actual generation-scoped
// Receive/history/profile and original-file authority ports.
// A nonnil owner is returned even on connect failure so Manager can drain it.
func InstallPrepared(ctx context.Context, host *plugins.WASMHost, guestID string, plan *jsjson.Object, ports InstallationPorts) (*Installation, error) {
	if ports.Registry == nil || ports.Registry.runtime == nil || ports.BuildReceive == nil || ports.Stopped == nil || ports.Sender.ReadOriginal == nil {
		return nil, errors.New("snowluma_installation_ports_incomplete")
	}
	if str(plan, "kind") != "install" {
		return nil, errors.New("snowluma_guest_startup_plan_required")
	}
	config, ok := get(plan, "config").(*jsjson.Object)
	if !ok {
		return nil, errors.New("invalid_guest_startup_config")
	}
	var receive func(context.Context, string) error
	callbacks := transport.HTTPCallbacks{Receive: func(ctx context.Context, raw string) error {
		if receive == nil {
			return errors.New("snowluma_receiver_not_installed")
		}
		return receive(ctx, raw)
	}, Status: ports.Status, Error: ports.Error}
	var http *transport.HTTP
	var ws *transport.WS
	var err error
	switch str(config, "transport") {
	case "http":
		port, _ := get(config, "eventPort").(float64)
		http, err = transport.NewHTTP(transport.HTTPConfig{URL: str(config, "url"), AccountID: str(config, "accountId"), Token: str(plan, "token"), EventToken: str(plan, "eventToken"), EventHost: str(config, "eventHost"), EventPath: str(config, "eventPath"), EventPort: int(port)}, host, guestID, callbacks)
	case "websocket":
		ws, err = transport.NewWS(transport.WSConfig{URL: str(config, "url"), AccountID: str(config, "accountId"), Token: str(plan, "token")}, host, guestID, callbacks)
	default:
		return nil, errors.New("invalid_guest_startup_transport")
	}
	if err != nil {
		return nil, err
	}
	sender, err := NewSender(host, guestID, "snowluma", str(config, "accountId"), ports.Sender, http, ws)
	if err != nil {
		if http != nil {
			_ = http.CloseAndWait(context.Background())
		} else {
			_ = ws.CloseAndWait(context.Background())
		}
		return nil, err
	}
	owner := &Installation{sender: sender, stopped: ports.Stopped}
	receive, err = ports.BuildReceive(sender)
	if err != nil {
		return owner, err
	}
	registration, err := ports.Registry.RegisterAdapter(sender)
	if err != nil {
		return owner, err
	}
	owner.registration = registration
	owner.detach = context.AfterFunc(ports.Sender.Life, owner.Dispose)
	if ports.Sender.Life.Err() != nil {
		owner.Dispose()
		return owner, errors.New("plugin_disabled")
	}
	// Source installs the adapter before connect, and an aborted/replaced setup
	// cannot leave a late successful connection advertised as current.
	if http != nil {
		err = http.Connect(ctx)
	} else {
		err = ws.Connect(ctx)
	}
	if err != nil {
		return owner, err
	}
	if err = sender.current(); err != nil {
		return owner, err
	}
	return owner, nil
}
func (i *Installation) Dispose() {
	i.once.Do(func() {
		if i.registration != nil {
			i.registration.Remove()
		} else {
			i.sender.Dispose()
		}
		i.stoppedErr = i.stopped()
	})
}
func (i *Installation) CloseAndWait(ctx context.Context) error {
	i.Dispose()
	if i.detach != nil {
		i.detach()
	}
	return errors.Join(i.sender.CloseAndWait(ctx), i.stoppedErr)
}
