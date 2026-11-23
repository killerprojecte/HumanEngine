package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func TestBuiltinDescriptorRegistersUnfamiliarIDWithoutLoadingArtifact(t *testing.T) {
	ctx := t.Context()
	m, _, _ := queueManager(t)
	host, err := plugins.NewWASMHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(ctx)
	if err = m.WithWASM(host); err != nil {
		t.Fatal(err)
	}
	loads := 0
	stopped := errors.New("fixture_artifact_not_present")
	db := BuiltinDescriptor{
		Manifest:     json.RawMessage(`{"id":"external-test-adapter","apiVersion":2,"configSchema":{"type":"object"}}`),
		LoadArtifact: func() ([]byte, error) { loads++; return nil, stopped },
		Start: func(context.Context, BuiltinRuntimeInput) (BuiltinLifetime, error) {
			t.Fatal("factory ran without a loaded artifact")
			return nil, nil
		},
	}
	if err := m.RegisterBuiltin(db); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterBuiltin(db); err == nil {
		t.Fatal("duplicate descriptor was accepted")
	}
	db.Manifest = json.RawMessage(`{"id":"another-adapter","apiVersion":2,"configSchema":{"type":"object"}}`)
	if err := m.RegisterBuiltin(db); err != nil {
		t.Fatal(err)
	}
	if got := m.RegisteredIDs(); !reflect.DeepEqual(got, []string{"external-test-adapter", "another-adapter"}) {
		t.Fatal(got)
	}
	if _, err := m.List(ctx); err != nil || loads != 0 {
		t.Fatalf("disabled plugin loaded its artifact: loads=%d err=%v", loads, err)
	}
	if err := m.Enable(ctx, "external-test-adapter", "descriptor"); err == nil || loads != 1 {
		t.Fatalf("enable did not invoke the descriptor loader once: loads=%d err=%v", loads, err)
	}
}
