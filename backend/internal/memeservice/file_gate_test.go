package memeservice

import (
	"bytes"
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestAdminFilePublicationRechecksSnapshotAndAuthority(t *testing.T) {
	f, s, _, _, _, original, asset := nativeImageFixture(t)
	ctx := context.Background()
	id := str(get(asset, "id"))
	snapshot, b, e := s.AdminFile(ctx, id)
	if e != nil || !bytes.Equal(b, original) {
		t.Fatal(e)
	}
	if _, e = s.Update(ctx, id, fields("favorite", true)); e != nil {
		t.Fatal(e)
	}
	emitted := false
	e = s.PublishAsset(ctx, id, snapshot, b, func([]byte) error { emitted = true; return nil })
	if e == nil || emitted {
		t.Fatal("stale file snapshot published", e)
	}
	snapshot, b, e = s.AdminFile(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.PublishAsset(ctx, id, snapshot, b, func(out []byte) error {
		emitted = true
		if !bytes.Equal(out, original) {
			t.Fatal("file changed")
		}
		return nil
	}); e != nil || !emitted {
		t.Fatal(e)
	}
	root, end, e := f.authority.OperationContext(ctx, f.main)
	if e != nil {
		t.Fatal(e)
	}
	defer end()
	_, e = f.authority.BeginDurable(root, f.main, func(*recovery.WriteSession) (recovery.Revocation, error) {
		return recovery.Source{SceneID: "s", MessageID: "m"}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	emitted = false
	e = s.PublishAsset(ctx, id, snapshot, b, func([]byte) error { emitted = true; return nil })
	if e == nil || emitted {
		t.Fatal("unACKed authority published file", e)
	}
}
