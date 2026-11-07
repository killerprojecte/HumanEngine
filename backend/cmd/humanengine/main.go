// Command humanengine composes the Go server. Preview mode is read-only until parity is complete.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/httpapi"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() (result error) {
	database := flag.String("database", "data/humanengine.sqlite", "existing installation database")
	address := flag.String("listen", "127.0.0.1:3001", "HTTP listen address")
	preview := flag.Bool("read-only-preview", false, "run the implemented query slice; not a production bot replacement")
	inspect := flag.Bool("inspect-builtins", false, "load distributed WASM artifacts and print their manifests without Node or a database")
	flag.Parse()
	if *inspect {
		return inspectBuiltins()
	}
	if !*preview {
		return fmt.Errorf("Go migration is incomplete: use --read-only-preview on a test copy; existing production startup is unchanged")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logs := observability.New(os.Stdout, os.Stderr)
	defer func() { result = errors.Join(result, logs.Close(context.Background())) }()
	app, err := httpapi.Open(ctx, *database, os.Getenv("HUMANENGINE_ADMIN_TOKEN"), logs)
	if err != nil {
		return err
	}
	defer app.Close()
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: app, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	_ = logs.Emit(observability.Event{Level: "info", Module: "server", Code: "server.listening", Message: "Go read-only preview ready", Fields: map[string]any{"address": listener.Addr().String()}})
	select {
	case <-ctx.Done():
		return server.Shutdown(context.Background())
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func inspectBuiltins() error {
	ctx := context.Background()
	host, err := plugins.NewWASMHost(ctx)
	if err != nil {
		return err
	}
	defer host.Close(ctx)
	manifests := []json.RawMessage{}
	for _, id := range []string{"typing-delay", "web-read", "web-search", "snowluma"} {
		artifact, err := plugins.BuiltinArtifact(id)
		if err != nil {
			return err
		}
		if err = host.Enable(ctx, id, artifact); err != nil {
			return err
		}
		output, err := host.Call(ctx, id, []byte(`{"operation":"manifest"}`))
		if err != nil {
			return err
		}
		manifests = append(manifests, json.RawMessage(output))
		if err = host.Disable(ctx, id); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(manifests)
}
