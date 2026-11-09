package learningowner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/contextuallearning"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestContextualNativeOwnerActualProposalAndEmbedding(t *testing.T) {
	var embeds atomic.Int32
	o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "embeddings") {
			t.Error("unexpected provider", r.URL.Path)
		}
		embeds.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	})
	var settings map[string]any
	if e := json.Unmarshal(contract.DefaultSettingsInput(), &settings); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(settings)
	if _, e := f.main.Exec("INSERT INTO config VALUES('settings',?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	p, e := o.NewContextual(func(a, b string) (int, error) { return strings.Compare(a, b), nil })
	if e != nil {
		t.Fatal(e)
	}
	defer p.CloseAndWait(context.Background())
	message := obj("id", "m", "sceneId", "s", "senderId", "p", "origin", "live", "status", "available", "parts", []any{obj("type", "text", "text", "HTTP defines request methods.")}, "sentAt", "2026-10-28T12:00:00.000Z", "receivedAt", "2026-10-28T12:00:00.000Z")
	submission := contextuallearning.Submission{SceneID: "s", SourceSnapshots: []*Object{message}, Input: obj("content", "HTTP defines request methods.", "kind", "fact", "sourceMessageIds", []any{"m"})}
	r, e := p.Submit(context.Background(), submission)
	if e != nil || r.Status != "queued" {
		t.Fatal(r, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = p.WhenIdle(ctx); e != nil {
		t.Fatal(e)
	}
	if p.Stats().Completed != 1 {
		t.Fatal(p.Stats())
	}
	var memories int
	if e = f.main.QueryRow("SELECT count(*) FROM memories").Scan(&memories); e != nil {
		t.Fatal(e)
	}
	if memories != 1 || embeds.Load() != 1 || indexCount(t, f) != 1 {
		t.Fatal(memories, embeds.Load(), indexCount(t, f))
	}
	// A caller can supply read authority valid before execution but revoked in the
	// physical writer. The concrete commit must call that same scoped guard.
	submission.Input = obj("content", "HTTP contains status codes.", "kind", "fact", "sourceMessageIds", []any{"m"})
	var guarded atomic.Int32
	submission.Read = &contextuallearning.ReadGuard{Identity: "revoked-in-writer", Valid: func(_ context.Context, ws *recovery.WriteSession) (bool, error) {
		if ws != nil {
			guarded.Add(1)
			return false, nil
		}
		return true, nil
	}}
	r, e = p.Submit(context.Background(), submission)
	if e != nil || r.Status != "queued" {
		t.Fatal(r, e)
	}
	if e = p.WhenIdle(ctx); e != nil {
		t.Fatal(e)
	}
	if guarded.Load() != 1 {
		t.Fatal("guard never reached actual writer", guarded.Load())
	}
	if e = f.main.QueryRow("SELECT count(*) FROM memories").Scan(&memories); e != nil {
		t.Fatal(e)
	}
	if memories != 1 || embeds.Load() != 1 {
		t.Fatal("revoked proposal escaped", memories, embeds.Load())
	}
	if e = p.CloseAndWait(ctx); e != nil {
		t.Fatal(e)
	}
}
