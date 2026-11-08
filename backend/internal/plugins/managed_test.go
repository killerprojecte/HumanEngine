package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManagedAdapterStartsAfterRegistration(t *testing.T) {
	for _, mode := range []string{"ready", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var ready atomic.Bool
			var received atomic.Int32
			h, err := NewV1HostWithOptions("node", io.Discard, V1HostOptions{AdapterServices: AdapterServices{
				IsRecalled: func(context.Context, AdapterMetadata, string, string) (bool, error) { return false, nil },
				Receive: func(context.Context, AdapterMetadata, dispatch.SceneRef, json.RawMessage) error {
					if !ready.Load() {
						t.Error("adapter started before registrations")
					}
					received.Add(1)
					return nil
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close(context.Background())
			path, _ := filepath.Abs("testdata/adapter-fixture.mjs")
			trace := filepath.Join(t.TempDir(), "trace")
			config, _ := json.Marshal(map[string]any{"trace": trace})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err = h.EnableManagedJSON(ctx, "adapter-fixture", path, config, "managed", func(raw json.RawMessage) error {
				traceBefore, _ := os.ReadFile(trace)
				if strings.Contains(string(traceBefore), "start\n") {
					t.Error("adapter start ran before registration ACK")
				}
				if received.Load() != 0 {
					t.Error("early delivery")
				}
				if mode == "failure" {
					return errors.New("registration_failed")
				}
				if mode == "cancel" {
					cancel()
					return nil
				}
				ready.Store(true)
				return nil
			})
			if mode == "ready" {
				if err != nil || received.Load() != 1 {
					t.Fatal(err, received.Load())
				}
			} else {
				traceAfter, _ := os.ReadFile(trace)
				if strings.Contains(string(traceAfter), "start\n") {
					t.Error("failed registration admitted adapter start")
				}
				if err == nil || received.Load() != 0 || h.PID() != 0 || len(h.RegisteredAdapters()) != 0 {
					t.Fatal("failed startup retained work", err, received.Load(), h.PID())
				}
			}
		})
	}
}
