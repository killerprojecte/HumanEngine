package httpapi

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"testing"
	"time"
)

func TestMemeToolInheritedRootWaitsForWriterAndSettlement(t *testing.T) {
	_, db, engine := memeHTTP(t)
	db.SetMaxOpenConns(1)
	root, release, e := engine.RootContext(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	// Keep the real only connection occupied, exposing an asynchronous inherited
	// root return deterministically through database/sql's waiter counter.
	held, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	before := db.Stats().WaitCount
	returned, settled := make(chan struct{}), make(chan error, 1)
	ext := engine.ComposeExtension(botruntime.Extension{})
	turn := &botruntime.TurnContext{Mode: "simulation", Scene: botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "sim:test", Kind: "simulation"}}}
	go func() {
		defer close(returned)
		ext.Execute(root, "memes.search", memeObject("query", ""), turn, func(_ any, e error) { settled <- e })
	}()
	deadline := time.Now().Add(3 * time.Second)
	for db.Stats().WaitCount == before {
		if time.Now().After(deadline) {
			t.Fatal("tool never waited for real SQLite connection")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-returned:
		t.Fatal("Execute returned while inherited owner was still required")
	default:
	}
	if e = held.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-settled:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tool failed to settle")
	}
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not return after settlement")
	}
}
