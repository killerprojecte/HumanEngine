package externaladapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/outboundmedia"
)

func TestOneBotOriginalScopeAndFinalQualification(t *testing.T) {
	ctx := t.Context()
	bytes := []byte("synthetic original encoder input")
	hash := sha256.Sum256(bytes)
	id := hex.EncodeToString(hash[:])
	path := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(path, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := outboundmedia.NewManager(outboundmedia.Config{
		CapacityMiB:     32,
		AvailableMemory: func() uint64 { return uint64(1024 * outboundmedia.MiB) },
	})
	if err != nil {
		t.Fatal(err)
	}
	encoder := manager.NewEncoder()
	defer encoder.CloseAndWait(context.Background())
	unexpected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid or withdrawn original reached the physical peer")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer unexpected.Close()
	profile := httpProfile{
		scope:  scope{adapter: "fixture", account: "42", onebot: true},
		action: unexpected.URL, onebotHTTP: &oneBotHTTP{outbound: true},
	}
	scene, err := profile.scene(peer{Kind: "group", ID: "900"})
	if err != nil {
		t.Fatal(err)
	}
	peerID := "901"
	var lookups int
	withdrawn := false
	session := &httpSession{
		profile: profile, life: ctx, encoder: encoder, client: unexpected.Client(),
		call: func(context.Context, context.Context, []byte) ([]byte, error) {
			return []byte(`{"action":"send_group_msg","params":{"group_id":"` + peerID + `","message":[{"type":"image","data":{"file":""}}]},"originals":[{"part":0,"index":0}]}`), nil
		},
		source: sourcePorts{original: func(_ context.Context, scope, asset string) (memeservice.Original, error) {
			lookups++
			if scope != scene.ID || asset != id {
				t.Fatalf("resolved unauthorized original: %q %q", scope, asset)
			}
			if withdrawn {
				return memeservice.Original{}, memeservice.ErrOriginalUnavailable
			}
			return memeservice.Original{Path: path, Bytes: float64(len(bytes)), Hash: id}, nil
		}},
	}
	part := dispatch.Segment{Parts: []json.RawMessage{json.RawMessage(`{"type":"media","mediaType":"image","assetId":"` + id + `"}`)}}
	if _, err := session.transmit(ctx, scene, part, nil); !errors.Is(err, errSendOriginal) || lookups != 0 {
		t.Fatalf("wrong-peer plan: %v, lookups=%d", err, lookups)
	}
	peerID = "900"
	if _, err := session.transmit(ctx, scene, part, func() error { withdrawn = true; return nil }); !errors.Is(err, errSendOriginal) || lookups != 2 {
		t.Fatalf("withdrawn original: %v, lookups=%d", err, lookups)
	}
	invalid := dispatch.Segment{Parts: []json.RawMessage{json.RawMessage(`{"type":"media","mediaType":"image","assetId":"` + strings.Repeat("x", 64) + `"}`)}}
	if _, err := session.transmit(ctx, scene, invalid, nil); !errors.Is(err, errSendOriginal) || lookups != 2 {
		t.Fatalf("unmanaged original: %v, lookups=%d", err, lookups)
	}
}
