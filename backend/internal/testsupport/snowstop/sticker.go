package snowstop

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

const (
	oldStickerDonor   = "old-contextual-sticker-source-witness"
	freshStickerDonor = "fresh-contextual-sticker-source-witness"
	freshStickerTag   = "fresh-contextual-sticker-label"
)

func stickerParts(t *testing.T, donor string, red uint8) ([]byte, []any) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 7, 6))
	img.SetNRGBA(2, 3, color.NRGBA{R: red, G: 134, B: 216, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	return raw, []any{
		map[string]any{"type": "text", "data": map[string]any{"text": donor}},
		map[string]any{"type": "mface", "data": map[string]any{"base64": base64.StdEncoding.EncodeToString(raw)}},
	}
}

func stickerAssetPath(scene string, raw []byte) string {
	identity := fmt.Sprintf(`[%q,"%x"]`, scene, sha256.Sum256(raw))
	return fmt.Sprintf("/api/memes/%x", sha256.Sum256([]byte(identity)))
}

type stickerState struct {
	ID                                           string
	Analyzed, Available, Shared, PrivacyReviewed bool
	SelectionOrigin, SharingOrigin               *string
	Tags                                         map[string]json.RawMessage
}

func (s stickerState) unlearned() bool {
	if s.Analyzed || s.Available || s.Shared || s.PrivacyReviewed ||
		s.SelectionOrigin != nil || s.SharingOrigin != nil || len(s.Tags) == 0 {
		return false
	}
	for k, v := range s.Tags {
		if k == "ocr" {
			if string(v) != `""` {
				return false
			}
		} else if string(v) != "[]" {
			return false
		}
	}
	return true
}

type stickerPeer struct {
	oldArrived, oldClosed chan struct{}
	oldCalls, freshCalls  atomic.Int32
}

func (p *stickerPeer) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	wire, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
		return
	}
	var req struct{ Model string }
	if err := json.Unmarshal(wire, &req); err != nil || req.Model != "native-sticker-witness" ||
		!bytes.Contains(wire, []byte("data:image/png;base64,")) {
		t.Error("contextual sticker request lacked its distinct model or inline image")
		http.Error(w, "invalid sticker fixture request", 400)
		return
	}
	switch {
	case bytes.Contains(wire, []byte(oldStickerDonor)):
		if p.oldCalls.Add(1) != 1 {
			t.Error("old sticker model request was repeated")
			http.Error(w, "replayed sticker fixture request", 500)
			return
		}
		close(p.oldArrived)
		<-r.Context().Done()
		close(p.oldClosed)
	case bytes.Contains(wire, []byte(freshStickerDonor)):
		if p.freshCalls.Add(1) != 1 {
			t.Error("fresh sticker model request was repeated")
		}
		content := fmt.Sprintf(`{"appearance":[],"style":[],"subject":[],"emotion":[],"intent":[%q],"tone":[],"intensity":[],"memeMeaning":[],"ocr":"","selection":{"usable":true,"shareable":false,"confidence":0.99,"containsPersonalData":false,"sourceSpecific":false,"containsRealPerson":false}}`, freshStickerTag)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": content}}}})
	default:
		t.Error("sticker fixture provider received neither contextual donor marker")
		http.Error(w, "missing sticker donor", 400)
	}
}
