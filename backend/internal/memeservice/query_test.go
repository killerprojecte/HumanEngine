package memeservice

import (
	"context"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"strings"
	"testing"
)

func newTestService(t *testing.T) (memeFixture, *Service) {
	t.Helper()
	f := newMemeFixture(t)
	p, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	store, e := assertions.New(f.main, f.authority, assertions.Options{DateParser: p})
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(context.Background(), f.main, f.authority, store, assertionauthority.Options{DateParser: p, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }}, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return f, s
}
func TestRecognitionNarrowsBeforeDecodeUsingHashIndex(t *testing.T) {
	f, s := newTestService(t)
	ctx := context.Background()
	saveMemeScene(t, f, fields("id", "s", "adapterId", "a", "accountId", "b", "learningEnabled", true))
	e := s.transaction(ctx, func(p *scope) error {
		// Scalars are valid SQLite JSON but not valid MemeAsset objects. Decoding even
		// one of these irrelevant rows would fail the service. No timing threshold.
		for i := 0; i < 4096; i++ {
			if _, e := p.ws.Exec("INSERT INTO meme_assets VALUES(?,?)", fmt.Sprintf("unrelated-%d", i), "0"); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	b := []byte("synthetic content")
	out, e := s.RecognizeBytes(ctx, Context{SceneID: "s"}, b)
	if e != nil || out != nil {
		t.Fatalf("unrelated decode: %v %v", out, e)
	}
	id := identity("s", digest(b))
	a := fields("id", id, "contentHash", digest(b), "sources", []any{fields("sceneId", "s", "messageId", "m", "senderId", "p")}, "tags", emptyTags(), "disabled", false)
	if e = s.transaction(ctx, func(p *scope) error { return p.write(a) }); e != nil {
		t.Fatal(e)
	}
	out, e = s.RecognizeBytes(ctx, Context{SceneID: "s"}, b)
	if e != nil || str(get(out, "id")) != id {
		t.Fatal(out, e)
	}
	rows, e := f.main.Query("EXPLAIN QUERY PLAN SELECT value FROM meme_assets WHERE COALESCE(json_extract(value,'$.contentHash'),id)=? AND ((json_extract(value,'$.shared')=1 AND json_extract(value,'$.privacyReviewed')=1 AND COALESCE(json_extract(value,'$.knownOnly'),0)=0) OR id IN (SELECT asset_id FROM meme_sources WHERE scene_id=?))", digest(b), "s")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if e = rows.Scan(&id, &parent, &unused, &detail); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(detail, "meme_content_hash_lookup") {
			indexed = true
		}
	}
	if !indexed {
		t.Fatal("content hash index unused")
	}
}
