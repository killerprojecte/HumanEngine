package app

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

type applicationLogClose struct {
	done        chan struct{}
	healthErr   error
	physicalErr error
}

// Logger returns this application's actual owner, including its default source
// StoreLogger. Producers and launchers must not construct a parallel logger.
func (a *Application) Logger() observability.LogOwner { return a.options.Logs }

// Shutdown drains producers and the default, owned source logger before
// releasing the installation. Explicitly supplied Logs remains borrowed.
func (a *Application) Shutdown(ctx context.Context) error { return a.shutdown(ctx, false) }

// ShutdownAndFlush explicitly closes/joins a supplied borrowed logger too. If
// the caller already finalized via borrowed Shutdown, that earlier installation
// release is preserved; the default owned path never releases before logging.
func (a *Application) ShutdownAndFlush(ctx context.Context) error { return a.shutdown(ctx, true) }
func (a *Application) shutdown(ctx context.Context, closeBorrowed bool) error {
	a.requests.Seal()
	// A completed operation remains observable under an already canceled waiter.
	select {
	case a.shutdownGate <- struct{}{}:
	default:
		select {
		case a.shutdownGate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { <-a.shutdownGate }()
	if !a.finalized && !a.producersDrained {
		if err := a.drainProducers(ctx); err != nil {
			return err
		}
		a.producersDrained = true
		emit(a.options.Logs, observability.Event{Level: "info", Module: "app", Code: "app.drained", Message: "Native application owners drained"})
	}
	if (a.options.ownsLogs || closeBorrowed) && !a.logsFinalized {
		complete, err := a.finishLogging(ctx)
		if !complete {
			return errors.Join(a.finalErr, err)
		}
		a.finalErr = errors.Join(a.finalErr, err)
	}
	if !a.finalized {
		if a.releaseInstallation != nil {
			a.finalErr = errors.Join(a.finalErr, a.releaseInstallation())
		}
		a.finalized = true
	}
	return a.finalErr
}
func (a *Application) finishLogging(ctx context.Context) (bool, error) {
	if a.options.Logs == nil {
		a.logsFinalized = true
		return true, nil
	}
	if a.loggingClose == nil {
		operation := &applicationLogClose{done: make(chan struct{})}
		a.loggingClose = operation
		logs := a.options.Logs
		// One native operation per application, never one per caller or record.
		// A caller may detach; the accepted logical close and physical join continue.
		go func() {
			operation.healthErr = logs.Close(context.Background())
			operation.physicalErr = logs.WaitPhysicalClose(context.Background())
			close(operation.done)
		}()
	}
	operation := a.loggingClose
	select {
	case <-operation.done:
	default:
		select {
		case <-operation.done:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if operation.physicalErr != nil {
		return false, errors.Join(operation.healthErr, operation.physicalErr)
	}
	a.logsFinalized = true
	return true, operation.healthErr
}
