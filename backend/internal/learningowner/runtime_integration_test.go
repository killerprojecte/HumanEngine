//go:build humanengine_icu && cgo

package learningowner

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcreteRuntimeNewIngressPersistenceAndExtensionCoexistence(t *testing.T) {
	var calls, received atomic.Int32
	o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "embeddings") {
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
			return
		}
		args := `{"content":"HTTP defines request methods.","kind":"fact","sourceMessageIds":["m"]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"content": "", "tool_calls": []any{map[string]any{"id": "proposal", "type": "function", "function": map[string]any{"name": "learning__propose", "arguments": args}}}}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
	})
	var settings map[string]any
	if e := json.Unmarshal(contract.DefaultSettingsInput(), &settings); e != nil {
		t.Fatal(e)
	}
	settings["learningBatchMs"], settings["learningMaxWaitMs"], settings["learningMinIntervalMs"] = 100, 100, 0
	raw, _ := json.Marshal(settings)
	if _, e := f.main.Exec("INSERT INTO config VALUES('settings',?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	detach := o.d.Foreground.AddExtension(&botruntime.Extension{Received: func(*Object, botruntime.Scene) error { received.Add(1); return nil }})
	defer detach()
	collator, e := topiccollation.New("en-US")
	if e != nil {
		t.Fatal(e)
	}
	defer collator.Close()
	d := o.d
	d.Options.Collator = collator
	live, e := New(d)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if e := live.CloseAndWait(ctx); e != nil {
			t.Error(e)
		}
	}()
	message := obj("id", "m", "sceneId", "s", "senderId", "p", "origin", "live", "status", "available", "parts", []any{obj("type", "text", "text", "HTTP defines request methods.")}, "sentAt", "2026-01-01T00:00:00.000Z", "receivedAt", "2026-01-01T00:00:00.000Z")
	if _, e = o.d.Foreground.Ingest(context.Background(), "s", message, false); e != nil {
		t.Fatal(e)
	}
	end := time.Now().Add(4 * time.Second)
	for live.Coordinator.Status().Completed != 1 {
		if time.Now().After(end) {
			t.Fatalf("learning did not settle: %+v calls=%d", live.Coordinator.Status(), calls.Load())
		}
		time.Sleep(time.Millisecond)
	}
	var memories int
	if e = f.main.QueryRow("SELECT count(*) FROM memories").Scan(&memories); e != nil {
		t.Fatal(e)
	}
	if received.Load() != 1 || calls.Load() != 2 || memories != 1 || indexCount(t, f) != 1 {
		t.Fatalf("composition received=%d calls=%d memories=%d vectors=%d", received.Load(), calls.Load(), memories, indexCount(t, f))
	}
	checkIndexIntegrity(t, f)
	if e = live.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = o.d.Foreground.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
