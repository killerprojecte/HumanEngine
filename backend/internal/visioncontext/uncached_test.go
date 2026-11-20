package visioncontext

import (
	"context"
	"testing"
)

func TestUncachedDescriptionUsesSourceOwnerWithoutReadingOrWritingCache(t *testing.T) {
	cache := new(Cache)
	cache.Put("unrelated", "scene", "message", "not used")
	cfg := Config{SceneID: "scene", Mode: "real", VisionRoute: true, Uncached: true, Models: func() {}}
	turn := new(Turn)
	calls := 0
	ports := Ports{
		Recalled:        func(string, string) (bool, error) { return false, nil },
		SourceAvailable: func(Candidate) (bool, error) { return true, nil },
		ReadImage:       func(context.Context, Candidate) ([]byte, error) { return []byte{1}, nil },
		ImageDataURL:    func(context.Context, []byte) (string, error) { return "data:image/png;base64,AA==", nil },
		Complete: func(_ context.Context, messages []ChatMessage) (string, error) {
			calls++
			if len(messages) != 2 || messages[0].Content != SystemPrompt || len(messages[1].Images) != 1 {
				t.Fatal("uncached path did not use the normal source prompt")
			}
			return "  fixture  ", nil
		},
		AddDependency:       func(string) {},
		AddSourceValidation: func(Candidate) {},
	}
	messages := []*Message{{ID: "message", SceneID: "scene", Status: "available", Parts: []Part{{Type: "media", MediaType: "image", AssetID: "0"}}}}
	for i := 0; i < 2; i++ {
		result, err := Describe(context.Background(), turn, cfg, messages, cache, ports, nil)
		if err != nil || len(result.Images) != 1 || result.Images[0].Description != "fixture" {
			t.Fatalf("uncached description %v: %v", result, err)
		}
	}
	if calls != 2 || len(cache.entries) != 1 || len(turn.results) != 0 {
		t.Fatalf("uncached path retained observations: calls=%d cache=%d turnResults=%d", calls, len(cache.entries), len(turn.results))
	}
}
