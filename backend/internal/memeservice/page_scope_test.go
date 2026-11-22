package memeservice

import (
	"context"
	"database/sql"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
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

func TestCatalogCountAndPageKeepOnePublicationSnapshot(t *testing.T) {
	f, _ := newTestService(t)
	f.main.SetMaxOpenConns(2)
	ctx := t.Context()
	insert := func(id string) error {
		_, err := f.main.ExecContext(ctx, "INSERT INTO main.meme_assets(id,value) VALUES(?,?)", id, stringify(fields("id", id, "favorite", true)))
		return err
	}
	if err := insert("original"); err != nil {
		t.Fatal(err)
	}
	emitted := false
	queries := 0
	err := f.authority.Publish(ctx, f.main, func(c *sql.Conn) ([]byte, error) {
		page, err := readAdminPage(PageOptions{State: "favorite"}, func(q string, args ...any) (assertions.Rows, error) {
			queries++
			if strings.HasPrefix(q, "SELECT value") {
				// SQLite WAL allows the other physical connection to commit,
				// but the publication's already-open snapshot must not shift.
				if err := insert("later"); err != nil {
					return nil, err
				}
			}
			return c.QueryContext(ctx, q, args...)
		})
		if err != nil {
			return nil, err
		}
		if page.Total != 1 || len(page.Items) != 1 || str(get(page.Items[0], "id")) != "original" {
			t.Fatalf("count and page saw different snapshots: %+v", page)
		}
		return nil, nil
	}, func([]byte) error { emitted = true; return nil })
	if err != nil || !emitted || queries != 2 {
		t.Fatalf("publication err=%v emitted=%v queries=%d", err, emitted, queries)
	}
}
