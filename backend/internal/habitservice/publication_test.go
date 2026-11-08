package habitservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

const publicationRecord = `{"id":"h","kind":"sceneRule","sceneId":"s","content":"Synthetic","evidenceVersion":2,"sources":[],"weight":0,"confidence":0,"status":"invalidated","locked":false,"learnedAt":"2026-11-01T00:00:00.000Z"}`

func TestAdminPublicationRejectsCallbackChanges(t *testing.T) {
	for _, mode := range []string{"ordinary", "withdrawal"} {
		t.Run(mode, func(t *testing.T) {
			f := newHabitFixture(t)
			ctx := context.Background()
			var callback func()
			s, e := New(ctx, f.main, f.authority, Options{Now: func(*Service) float64 {
				if callback != nil {
					fn := callback
					callback = nil
					fn()
				}
				return nativeNow
			}})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.main.Exec("INSERT INTO habits VALUES('h','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?)", publicationRecord); e != nil {
				t.Fatal(e)
			}
			callback = func() {
				if mode == "ordinary" {
					_, e = f.main.Exec("UPDATE habits SET value=replace(value,'Synthetic','Changed')")
				} else {
					e = s.TransactionWithSession(ctx, func(_ *Service, ws *recovery.WriteSession) error {
						_, e := ws.AppendIntent(recovery.Source{SceneID: "s", MessageID: "m"})
						return e
					})
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			emitted := false
			e = s.PublishAdminList(ctx, func([]byte) error { emitted = true; return nil })
			if e == nil || emitted {
				t.Fatal("changed capture published", e, emitted)
			}
		})
	}
}
func TestHabitPublicationRequiresRootAndDoesNotHoldSocket(t *testing.T) {
	f := newHabitFixture(t)
	ctx := context.Background()
	now := nativeNow
	s := nativeService(t, f, &now)
	_, e := f.main.Exec("INSERT INTO habits VALUES('h','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?)", publicationRecord)
	if e != nil {
		t.Fatal(e)
	}
	e = s.Transaction(ctx, func(b *Service) error {
		if e := b.PublishAdminList(ctx, func([]byte) error { return nil }); e == nil {
			return errors.New("nested publication accepted")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.PublishAdminList(ctx, func(body []byte) error {
		if len(body) == 0 {
			return errors.New("missing bytes")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
