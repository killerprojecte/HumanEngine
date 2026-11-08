package providertransport

import (
	"context"
	"reflect"
	"testing"
)

func TestImageEnvelopeDoesNotInspectToolArguments(t *testing.T) {
	body := map[string]any{"tools": []any{map[string]any{"image_url": "data:image/png;base64,c3ludGhldGlj"}}, "messages": []any{map[string]any{"role": "tool", "content": "data:image/png;base64,c3ludGhldGlj"}}}
	if inlineImages(body) {
		t.Fatal("tool data treated as native image")
	}
	if _, e := (NativeImageSerializer{}).Serialize(context.Background(), body, nil, nil); e != nil {
		t.Fatal(e)
	}
}
func TestDefaultOpaqueImageAdmissionAndConfiguredCodec(t *testing.T) {
	body := map[string]any{"messages": []map[string]any{{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/opaque;base64,c3ludGhldGlj"}}}}}}
	before := body["messages"]
	if !inlineImages(body) {
		t.Fatal("native image not detected")
	}
	if _, e := (NativeImageSerializer{}).Serialize(context.Background(), body, nil, nil); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, body["messages"]) {
		t.Fatal("source modified")
	}
	if _, e := (NativeImageSerializer{}).Serialize(context.Background(), body, &ImageUploadLimits{}, nil); e == nil || e.Error() != "invalid_image_data" {
		t.Fatalf("configured decode bypassed: %v", e)
	}
}
func TestImageRequestCapWithoutImages(t *testing.T) {
	cap := 1
	_, e := (NativeImageSerializer{}).Serialize(context.Background(), map[string]any{"model": "synthetic"}, &ImageUploadLimits{MaxRequestBytes: &cap}, nil)
	if e == nil || e.Error() != "image_request_size_limit" {
		t.Fatal(e)
	}
}
