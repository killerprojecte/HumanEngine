package botruntime

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestNativeVisionOriginalCacheSourceGuard(t *testing.T) {
	r, authority, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {
		t.Error("byte cache started a model")
	}))
	authority.enrolled = true
	scene := fixtureScene(false)
	if err := r.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	var reads int
	var late, retired bool
	denied := errors.New("fixture read revoked")
	adapter := &Adapter{
		InlineImage: &InlineImageAdapter{
			Classify: func(Scene, string) NativeImageLink { return NativeImageBrowserDirect },
			Current:  func() bool { return !retired },
		},
		PrepareVisionImage: func(Scene, string) GuardedImageRead {
			return func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
				if err := before(ctx); err != nil {
					return nil, err
				}
				reads++
				if late {
					retired = true
				}
				return []byte{1, 2, 3}, nil
			}
		},
	}
	r.AddAdapter(scene.AdapterID, adapter)
	msg := fixtureMessage(scene.ID, "fixture-image", "fixture text")
	_ = msg.Set("parts", []any{obj("type", "media", "mediaType", "image/png", "assetId", "ob:media:fixture")})
	if _, err := r.IngestNativeMedia(t.Context(), adapter, scene, msg, false); err != nil {
		t.Fatal(err)
	}
	tc := &TurnContext{Scene: scene, owner: &turn{runtime: r}, sources: []*Object{msg}}
	var source *DisplayImageSource
	tc.NativeVisionSources(msg, func(s *DisplayImageSource, _ int) { source = s })
	if source == nil {
		t.Fatal("native vision source not retained")
	}
	allowed := true
	before := func(context.Context) error {
		if !allowed {
			return denied
		}
		return nil
	}
	read := func() ([]byte, error) { return source.BrowserImageBytes(t.Context(), before) }
	first, err := read()
	if err != nil || !bytes.Equal(first, []byte{1, 2, 3}) {
		t.Fatalf("cold read: %v, %v", first, err)
	}
	first[0] = 0
	allowed = false
	if got, err := read(); got != nil || !errors.Is(err, denied) || reads != 1 {
		t.Fatalf("hit bypassed authority: %v, %v, reads %d", got, err, reads)
	}
	allowed = true
	hit, err := read()
	if err != nil || !bytes.Equal(hit, []byte{1, 2, 3}) || reads != 1 {
		t.Fatalf("cache shared mutable input: %v, %v, reads %d", hit, err, reads)
	}
	// Use a fresh cache to model a late completion after this adapter has
	// fetched bytes, but before the bytes reach an authorized consumer.
	r.visionOriginals.clear()
	late = true
	if got, err := read(); got != nil || !errors.Is(err, ErrDisplayImageUnavailable) || reads != 2 || len(r.visionOriginals.entries) != 0 {
		t.Fatalf("retired fetch seeded cache: %v, %v, reads %d", got, err, reads)
	}
}
