package memeservice

import (
	"context"
	"testing"
)

func TestAdminPageCountsMainEmptyPopulatedFilteredAndShadow(t *testing.T) {
	f, s := newTestService(t)
	ctx := context.Background()
	check := func(o PageOptions, want int) {
		t.Helper()
		p, e := s.AdminPage(ctx, o)
		if e != nil || p.Total != want {
			t.Fatalf("page %+v: %+v %v want %d", o, p, e, want)
		}
	}
	check(PageOptions{}, 0)
	for _, id := range []string{"first", "second"} {
		a := fields("id", id, "sources", []any{}, "tags", emptyTags(), "favorite", id == "first", "shared", false)
		if _, e := f.main.Exec("INSERT INTO main.meme_assets(id,value) VALUES(?,?)", id, stringify(a)); e != nil {
			t.Fatal(e)
		}
	}
	check(PageOptions{}, 2)
	check(PageOptions{State: "favorite"}, 1)
	check(PageOptions{Scope: "shared"}, 0)
	if _, e := f.main.Exec("CREATE TEMP TABLE meme_assets(id TEXT,value TEXT);INSERT INTO temp.meme_assets VALUES('shadow','{}');CREATE TEMP TABLE meme_sources(asset_id TEXT,scene_id TEXT);CREATE TEMP TABLE meme_search_concepts(asset_id TEXT,concept TEXT)"); e != nil {
		t.Fatal(e)
	}
	check(PageOptions{}, 2)
	check(PageOptions{State: "favorite"}, 1)
	check(PageOptions{Scope: "scene:s"}, 0)
	emitted := false
	if e := s.PublishPage(ctx, PageOptions{}, func([]byte) error { emitted = true; return nil }); e != nil || !emitted {
		t.Fatal("main-qualified publication", e)
	}
}
