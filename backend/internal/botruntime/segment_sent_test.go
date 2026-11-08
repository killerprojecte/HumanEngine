package botruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	_ "github.com/mattn/go-sqlite3"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
)

func TestSegmentSentCallbackRetainsPhysicalOwner(t *testing.T) {
	r := new(Runtime)
	root, ok := r.work.Start()
	if !ok {
		t.Fatal("root admission")
	}
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	s := NewSegmentSentSequencer(func(Scene, string, dispatch.Segment, dispatch.Receipt) { close(entered); <-release; close(returned) })
	p := s.reserve(r, Scene{ID: "s"}, "t", dispatch.Segment{}, dispatch.Receipt{})
	if p == nil {
		t.Fatal("missing reservation")
	}
	root()
	r.work.Seal()
	p.Release()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := r.work.Wait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("callback escaped drain: %v", e)
	}
	close(release)
	<-returned
	if e := r.work.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestSegmentSentRecallIsCheckedAtExecution(t *testing.T) {
	r := new(Runtime)
	a := newTestAuthority()
	r.services.Authority = a
	root, _ := r.work.Start()
	result := make(chan dispatch.Segment, 1)
	s := NewSegmentSentSequencer(func(_ Scene, _ string, segment dispatch.Segment, _ dispatch.Receipt) { result <- segment })
	id := "out"
	p := s.reserve(r, Scene{ID: "s"}, "t", dispatch.Segment{Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"original"}`)}}, dispatch.Receipt{MessageID: &id})
	a.mu.Lock()
	a.recalled["s|out"] = true
	a.mu.Unlock()
	root()
	r.work.Seal()
	p.Release()
	got := <-result
	if len(got.Parts) != 0 {
		t.Fatalf("recalled payload escaped: %s", got.Parts)
	}
	if e := r.work.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestSegmentSentNoReceiptIDClonesPayload(t *testing.T) {
	r := new(Runtime)
	root, _ := r.work.Start()
	result := make(chan dispatch.Segment, 1)
	s := NewSegmentSentSequencer(func(_ Scene, _ string, segment dispatch.Segment, _ dispatch.Receipt) { result <- segment })
	raw := json.RawMessage(`{"type":"text","text":"original"}`)
	p := s.reserve(r, Scene{ID: "s"}, "t", dispatch.Segment{Parts: []json.RawMessage{raw}}, dispatch.Receipt{})
	raw[0] = 'X'
	root()
	r.work.Seal()
	p.Release()
	got := <-result
	if string(got.Parts[0]) != `{"type":"text","text":"original"}` {
		t.Fatal("published payload aliased caller")
	}
	if e := r.work.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
}

// An actual SQLite connection is held, rather than a sleep in the callback.
// Transport ACK and turn cleanup must settle while physical event SQL remains
// blocked; CloseAndWait must retain that owner until SQLite has returned.
func TestSegmentSentHeldSQLDoesNotDelayACKButPreventsShutdown(t *testing.T) {
	db, e := sql.Open("sqlite3", filepath.Join(t.TempDir(), "events.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, e = db.Exec("CREATE TABLE usage_events(value TEXT)"); e != nil {
		t.Fatal(e)
	}
	held, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	var replies atomic.Int32
	model := ModelFunc(func(_ context.Context, req ModelRequest, settle func(Completion, error)) {
		switch req.Stage {
		case "decision":
			settle(fixtureDecision("reply"), nil)
		case "plan":
			settle(fixturePlan(req, "reply", []any{}), nil)
		case "replyer":
			if replies.Add(1) == 1 {
				settle(completion([]replyplan.ToolCall{fixtureSend("Synthetic reply.", false)}, ""), nil)
			} else {
				settle(completion(nil, ""), nil)
			}
		default:
			settle(Completion{}, code("unexpected_stage"))
		}
	})
	r, _, _ := testRuntime(t, model)
	entered, finished := make(chan struct{}), make(chan error, 1)
	r.AddExtension(&Extension{SegmentSentSequencers: []*SegmentSentSequencer{NewSegmentSentSequencer(func(Scene, string, dispatch.Segment, dispatch.Receipt) {
		close(entered)
		_, e := db.Exec("INSERT INTO usage_events VALUES('sent')")
		finished <- e
	})}})
	topic := registerFixture(t, r)
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		if e := before(); e != nil {
			return dispatch.Rejected[dispatch.Receipt](e)
		}
		return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent})
	}}})
	turn, e := r.Run("scene", topic, false)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-entered:
	case <-turn.Done:
		if result := turn.Wait(); result.Err != nil {
			t.Fatalf("turn failed before callback: %+v", result)
		}
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("acknowledged turn never published callback")
		}
	}
	select {
	case <-turn.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("held business SQL delayed acknowledged turn")
	}
	if result := turn.Wait(); result.Err != nil {
		t.Fatal(result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = r.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("SQLite callback owner lost: %v", e)
	}
	if e = held.Close(); e != nil {
		t.Fatal(e)
	}
	if e = <-finished; e != nil {
		t.Fatal(e)
	}
	if e = r.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM usage_events").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
