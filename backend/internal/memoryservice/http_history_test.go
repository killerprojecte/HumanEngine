package memoryservice

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Interleave a real recall after legacy/current anchor preparation and before
// opening the final publication snapshot. The old prepared anchor is not a
// permission to return its observed body.
func TestAdminHistoryRequalifiesAfterPreparedAnchor(t *testing.T) {
	ctx := t.Context()
	s, f := serviceSetup(t)
	_, err := f.main.Exec("UPDATE scenes SET value='{\"id\":\"s\",\"kind\":\"group\",\"adapterId\":\"adapter\",\"accountId\":\"account\"}' WHERE id='s'")
	exposureMust(t, err)
	r, err := s.Create(ctx, parseObject(t, `{"scope":{"kind":"scene","sceneId":"s"},"kind":"fact","content":"PRIVATE_OLD_WITNESS","sources":[{"sceneId":"s","messageId":"witness","senderId":"speaker"}]}`))
	exposureMust(t, err)
	p, err := recovery.NewReadPublisher(ctx, f.main, f.recovery)
	exposureMust(t, err)
	t.Cleanup(func() { exposureMust(t, p.CloseAndWait(context.Background())) })
	a, err := s.BindAdminRead(p)
	exposureMust(t, err)
	query := &HistoryQuery{SceneID: "s", ID: text(r, "id")}
	capture := func() (int, string) {
		t.Helper()
		var code int
		var body string
		started, err := a.PublishHistory(ctx, query, func(status int, emitted []byte) error {
			code, body = status, string(emitted)
			return nil
		})
		exposureMust(t, err)
		if !started {
			t.Fatal("publication did not start")
		}
		return code, body
	}
	if code, body := capture(); code != http.StatusOK || !strings.Contains(body, "PRIVATE_OLD_WITNESS") {
		t.Fatalf("pre-recall publication %d %s", code, body)
	}
	publish := a.publish
	mutated := false
	a.publish = func(ctx context.Context, read func(*assertions.Reader) ([]byte, error), emit func([]byte) error) error {
		mutated = true
		_, err := s.Recall(ctx, "s", "witness")
		if err != nil {
			return err
		}
		return publish(ctx, read, emit)
	}
	if code, body := capture(); !mutated || strings.Contains(body, "PRIVATE_OLD_WITNESS") || code != http.StatusOK && code != http.StatusNotFound {
		t.Fatalf("a recalled body survived qualification: %d %s", code, body)
	}
}
