package habitengine

import (
	"context"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"sync"
	"testing"
	"time"
)

func TestOwnedSourceLifetimeAndForeignBinding(t *testing.T) {
	f, s := engineFixture(t)
	other, _ := engineFixture(t)
	ctx := context.Background()
	m := object("id", "m", "sceneId", "s", "senderId", "p", "origin", "live", "status", "available", "parts", []any{})
	var saved *sourceevents.Store
	var capability *recovery.WriteSession
	e := s.habits.TransactionWithSession(ctx, func(h *habitservice.Service, ws *recovery.WriteSession) error {
		capability = ws
		saved = f.events.WithSession(ws)
		if _, e := other.events.WithSession(ws).CurrentRuntimeSnapshot(ctx, m); e == nil {
			t.Fatal("foreign accepted")
		}
		if valid, e := saved.CurrentRuntimeSnapshot(ctx, m); e != nil || !valid {
			t.Fatalf("owned %v %v", valid, e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := saved.CurrentRuntimeSnapshot(ctx, m); e == nil {
		t.Fatal("expired accepted")
	}
	if e := capability.AssertReady(); e == nil {
		t.Fatal("escaped writer accepted")
	}
	// A normal SQL error remains catchable, not a poisoned root transaction.
	e = s.habits.TransactionWithSession(ctx, func(h *habitservice.Service, ws *recovery.WriteSession) error {
		if _, e := ws.Exec("INSERT INTO nonexistent VALUES(1)"); e == nil {
			t.Fatal("fixture error missing")
		}
		_, e := f.events.WithSession(ws).CurrentRuntimeSnapshot(ctx, m)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestEditCannotSplitEvidenceAndHabitWrite(t *testing.T) {
	f, s := engineFixture(t)
	ctx := context.Background()
	tt := turn()
	scene := `{"id":"s","kind":"group","adapterId":"synthetic","accountId":"account","platformSceneId":"s","name":"Synthetic","participationEnabled":true,"learningEnabled":true,"proactiveEnabled":false}`
	if _, e := f.main.Exec("INSERT INTO scenes VALUES('s',?)", scene); e != nil {
		t.Fatal(e)
	}
	if _, e := f.main.Exec("INSERT INTO config VALUES('settings',?)", enabledSettings(t)); e != nil {
		t.Fatal(e)
	}
	m := object("id", "m", "sceneId", "s", "senderId", "p", "origin", "live", "status", "available", "sentAt", "2026-11-02T00:00:00.000Z", "parts", []any{object("type", "text", "text", "assistant always use concise replies")})
	_, captured, e := s.authority.Observe(ctx, tt.Scene, m, nil)
	if e != nil {
		t.Fatal(e)
	}
	tt.SourceSnapshots = []*Object{captured}
	start := make(chan struct{})
	attempt := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	edited := copyValue(m).(*Object)
	_ = edited.Set("parts", []any{object("type", "text", "text", "assistant always use detailed replies")})
	go func() {
		<-start
		close(attempt)
		status, _, e := s.authority.Observe(ctx, tt.Scene, edited, object("editId", "edit-1", "order", object("kind", "platform_sequence", "sequence", float64(1))))
		if e == nil && status != "edited" {
			e = fmt.Errorf("edit status %s", status)
		}
		done <- e
	}()
	h, e := habitservice.New(ctx, f.main, f.authority, habitservice.Options{ID: func() (string, error) { return "interleaved-habit", nil }, Now: func(*habitservice.Service) float64 {
		once.Do(func() {
			close(start)
			<-attempt
			select {
			case e := <-done:
				t.Fatalf("edit crossed owned proof/write reservation: %v", e)
			case <-time.After(25 * time.Millisecond):
			}
		})
		return 1793577600000
	}})
	if e != nil {
		t.Fatal(e)
	}
	s.habits = h
	r, e := s.ToolPropose(ctx, tt, object("kind", "botExpression", "preference", "concise", "direction", float64(1), "sourceMessageIds", []any{"m"}))
	if e != nil || str(r, "status") != "saved" {
		t.Fatalf("first write %v %v", r, e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	// Captured old text/ref must not be blessed by the newly current revision.
	r, e = s.ToolPropose(ctx, tt, object("kind", "botExpression", "preference", "concise", "direction", float64(1), "sourceMessageIds", []any{"m"}))
	if e != nil || str(r, "status") != "rejected" {
		t.Fatalf("stale capture %v %v", r, e)
	}
}
