package memoryservice

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// The TS route runs the successful transaction, refresh and body build without
// an await. Native owners can legitimately interleave at publication; a held
// reply must not serialize a row whose evidence was recalled in that gap.
func TestRelationReviewPostCommitPublication(t *testing.T) {
	for _, race := range []string{"recall", "cancel"} {
		t.Run(race, func(t *testing.T) {
			s, f, options := outerSetup(t)
			_, err := f.main.Exec(`UPDATE scenes SET value=json_set(value,'$.kind','group') WHERE id='s';
CREATE TABLE IF NOT EXISTS config(key TEXT PRIMARY KEY,value TEXT NOT NULL);
INSERT INTO config(key,value) VALUES('settings',json_set(?,'$.memoryGraphEnabled',json('true')))`, string(contract.DefaultSettingsInput()))
			exposureMust(t, err)
			in, candidate := outerInput(t, "review-witness")
			_ = candidate.Set("relations", []any{outerRelation(t)})
			result, err := s.Propose(t.Context(), in, candidate, options)
			requireOuterSaved(t, result, err)
			if result.Relations == nil || len(result.Relations.Created) != 1 {
				t.Fatalf("proposal qualification failed: %+v", result.Relations)
			}
			id := text(result.Relations.Created[0], "id")
			const expectedVersion = 2
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			head, err := f.recovery.Head(ctx)
			exposureMust(t, err)

			p, err := recovery.NewReadPublisher(ctx, f.main, f.recovery)
			exposureMust(t, err)
			t.Cleanup(func() { exposureMust(t, p.CloseAndWait(context.Background())) })
			a, err := s.BindAdminRead(p)
			exposureMust(t, err)
			schemas, err := contract.NewSchemas()
			exposureMust(t, err)
			publish := a.publish
			interleaved := false
			a.publish = func(ctx context.Context, read func(*assertions.Reader) ([]byte, error), emit func([]byte) error) error {
				interleaved = true
				var persisted int
				var status string
				err := f.main.QueryRowContext(ctx, "SELECT version,json_extract(value,'$.status') FROM memory_relation_candidates WHERE id=?", id).Scan(&persisted, &status)
				exposureMust(t, err)
				currentHead, err := f.recovery.Head(ctx)
				exposureMust(t, err)
				if persisted != expectedVersion || status != "rejected" || currentHead != head {
					t.Fatalf("did not commit exactly one derived review before publication: %d %s %+v, previous head %+v", persisted, status, currentHead, head)
				}
				if race == "recall" {
					if _, err := s.Recall(ctx, "s", "review-witness"); err != nil {
						return err
					}
				} else {
					cancel()
				}
				return publish(ctx, read, emit)
			}
			var status int
			var reply string
			_, err = a.ReviewAndPublishRelation(ctx, &RelationReviewQuery{ID: id, SceneID: "s", Status: "rejected", ExpectedVersion: 1}, schemas, func(code int, body []byte) error {
				status, reply = code, string(body)
				return nil
			})
			if !interleaved {
				t.Fatalf("final publication was never attempted: %v", err)
			}
			if race == "cancel" {
				if !errors.Is(err, context.Canceled) || status != 0 || reply != "" {
					t.Fatalf("cancelled reply: status=%d body=%s err=%v", status, reply, err)
				}
				if outerCount(t, f, "SELECT count(*) FROM memory_relation_candidates WHERE id=? AND version=?", id, expectedVersion) != 1 {
					t.Fatal("committed relation was replayed or lost after cancellation")
				}
			} else if err != nil || status != http.StatusNotFound || reply != `{"error":"Relation unavailable"}` {
				t.Fatalf("recalled reply: status=%d body=%s err=%v", status, reply, err)
			}
		})
	}
}
