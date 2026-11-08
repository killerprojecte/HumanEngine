package memeservice

import (
	"bytes"
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeImageIntakeFileFaultAndWithdrawnSource(t *testing.T) {
	library := os.Getenv("HE_IMAGE_LIBRARY")
	if library == "" {
		t.Skip("HE_IMAGE_LIBRARY required for actual native libvips intake")
	}
	codec, e := imagecodec.New(library)
	if e != nil {
		t.Fatal(e)
	}
	f := newMemeFixture(t)
	ctx := context.Background()
	date, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	store, e := assertions.New(f.main, f.authority, assertions.Options{DateParser: date})
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(t.TempDir(), "media")
	s, e := New(ctx, f.main, f.authority, store, assertionauthority.Options{DateParser: date, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }}, root)
	if e != nil {
		t.Fatal(e)
	}
	saveMemeScene(t, f, mustObject(t, `{"id":"s","adapterId":"a","accountId":"c","learningEnabled":true}`))
	source := mustObject(t, `{"sceneId":"s","messageId":"m","senderId":"u"}`)
	ev := copyObject(source)
	set(ev, "available", true)
	c := Context{SceneID: "s", SourceEvidence: []*Object{ev}}
	img := image.NewNRGBA(image.Rect(0, 0, 5, 3))
	img.SetNRGBA(1, 1, color.NRGBA{255, 30, 12, 255})
	var buffer bytes.Buffer
	if e = png.Encode(&buffer, img); e != nil {
		t.Fatal(e)
	}
	b := buffer.Bytes()
	id := identity("s", digest(b))
	if e = os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	path, _ := s.file(id, "png")
	if e = os.WriteFile(path, []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Intake(ctx, c, b, source, false, codec); e == nil || e.Error() != "media_hash_mismatch" {
		t.Fatal(e)
	}
	all, e := s.AdminList(ctx)
	if e != nil || len(all) != 0 {
		t.Fatal(all, e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	a, e := s.Intake(ctx, c, b, source, false, codec)
	if e != nil {
		t.Fatal(e)
	}
	if get(a, "id") != id || get(a, "width") != float64(5) || get(a, "height") != float64(3) || len(arr(get(a, "sources"))) != 0 {
		t.Fatal(a)
	}
	if e = s.BindMediaReference(ctx, c, "opaque", id, source, b); e != nil {
		t.Fatal(e)
	}
	got, e := s.ReadReference(ctx, c, "opaque", "m")
	if e != nil || !bytes.Equal(got, b) {
		t.Fatal(e)
	}
	if _, e = f.main.Exec("INSERT INTO recall_tombstones VALUES(?,?,?)", "s", "m", "2026-11-08T00:00:00Z"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Intake(ctx, c, b, source, false, codec); e == nil || e.Error() != "invalid_source_evidence" {
		t.Fatal(e)
	}
}
