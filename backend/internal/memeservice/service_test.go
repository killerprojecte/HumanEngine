package memeservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"strings"
	"testing"
)

func TestTagsUTF16AndDedup(t *testing.T) {
	a := emptyTags()
	set(a, "intent", []any{"x", "x"})
	v, e := ValidateTags(a)
	if e != nil || len(arr(get(v, "intent"))) != 1 {
		t.Fatal(v, e)
	}
	set(a, "ocr", strings.Repeat("😀", 2001))
	if _, e = ValidateTags(a); e == nil {
		t.Fatal("UTF16 limit")
	}
}
func TestReferenceIdentityIncludesMessage(t *testing.T) {
	if identity("s", "r", "one") == identity("s", "r", "two") {
		t.Fatal("identity collision")
	}
}
func TestNativeStoreRestartWithdrawal(t *testing.T) {
	f := newMemeFixture(t)
	ctx := context.Background()
	p, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	store, e := assertions.New(f.main, f.authority, assertions.Options{DateParser: p})
	if e != nil {
		t.Fatal(e)
	}
	policy := assertionauthority.Options{DateParser: p, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }}
	s, e := New(ctx, f.main, f.authority, store, policy, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	saveMemeScene(t, f, mustObject(t, `{"id":"s","adapterId":"a","accountId":"c","learningEnabled":true}`))
	source := mustObject(t, `{"sceneId":"s","messageId":"m","senderId":"u"}`)
	evidence := copyObject(source)
	set(evidence, "available", true)
	set(evidence, "isBot", false)
	c := Context{SceneID: "s", SourceEvidence: []*Object{evidence}}
	b := []byte("synthetic bytes only")
	id := identity("s", digest(b))
	a := mustObject(t, `{"sources":[],"tags":{"appearance":[],"style":[],"subject":[],"emotion":[],"intent":[],"tone":[],"intensity":[],"memeMeaning":[],"ocr":""},"available":true,"analyzed":true,"analysisState":"running","usageCount":0}`)
	set(a, "id", id)
	set(a, "contentHash", digest(b))
	set(a, "sources", []any{source})
	if e = s.transaction(ctx, func(p *scope) error { return p.write(a) }); e != nil {
		t.Fatal(e)
	}
	if e = s.BindMediaReference(ctx, c, "opaque", id, source, b); e != nil {
		t.Fatal(e)
	}
	found, e := s.HasMediaReference(ctx, c, "opaque", "different")
	if e != nil || found {
		t.Fatal("cross-message alias", e)
	}
	out, e := s.LookupMediaReference(ctx, c, "opaque", "m")
	if e != nil || out == nil {
		t.Fatal(out, e)
	}
	if e = s.RecordUsage(ctx, c, id); e != nil {
		t.Fatal(e)
	}
	s, e = New(ctx, f.main, f.authority, store, policy, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	all, e := s.AdminList(ctx)
	if e != nil || get(all[0], "analysisState") != "interrupted" {
		t.Fatal(all, e)
	}
	_, e = f.main.Exec("INSERT INTO recall_tombstones VALUES(?,?,?)", "s", "m", "2026-11-08T00:00:00Z")
	if e != nil {
		t.Fatal(e)
	}
	out, e = s.LookupMediaReference(ctx, c, "opaque", "m")
	if e != nil || out != nil {
		t.Fatal("withdrawn source remains visible", out, e)
	}
}
