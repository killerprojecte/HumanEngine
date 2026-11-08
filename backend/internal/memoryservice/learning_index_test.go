package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestIndexabilityRefreshesUnsupportedRecordsInBothScopes(t *testing.T) {
	for _, session := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "writer"}[session], func(t *testing.T) {
			s, f := serviceSetup(t)
			putRecord(t, f, "unsupported", `,"lineageStatus":"verified"`)
			var got []*Object
			var e error
			if session {
				e = f.recovery.WithWriteSession(context.Background(), f.main, func(ws *recovery.WriteSession) error {
					got, e = s.IndexableByIDsInSession(context.Background(), ws, []string{"unsupported"})
					return e
				})
			} else {
				got, e = s.IndexableByIDs(context.Background(), []string{"unsupported"})
			}
			if e != nil || len(got) != 0 {
				t.Fatalf("indexability %v %v", got, e)
			}
			if e = f.store.Read(context.Background(), func(r *assertions.Reader) error {
				v, e := r.Record("unsupported")
				if e == nil && text(v, "status") != "invalidated" {
					t.Fatal("support refresh skipped durable invalidation", v)
				}
				return e
			}); e != nil {
				t.Fatal(e)
			}
		})
	}
}
