package memeservice

import (
	"context"
	"os"
	"testing"
)

func TestDeleteUnlinkFailureLeavesDurableGapAndOriginalRow(t *testing.T) {
	f, s, _, _, _, _, a := nativeImageFixture(t)
	id := str(get(a, "id"))
	path, e := s.file(id, str(get(a, "format")))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	// Node fs.unlink rejects even an EMPTY directory. os.Remove would incorrectly
	// remove it; the production operation must be unlink, not Remove's fallback.
	operation, release, e := f.authority.OperationContext(context.Background(), f.main)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if ok, e := s.AdminDelete(operation, id); ok || e == nil || e.Error() != "media_delete_failed" {
		t.Fatal(ok, e)
	}
	if info, e := os.Stat(path); e != nil || !info.IsDir() {
		t.Fatal("unlink unexpectedly removed directory", e)
	}
	var count int
	if e = f.main.QueryRow("SELECT COUNT(*) FROM meme_assets WHERE id=?", id).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	head, e := f.authority.Head(context.Background())
	if e != nil || head.Sequence != 1 {
		t.Fatal(head, e)
	}
	if _, e = s.AdminGet(context.Background(), id); e == nil {
		t.Fatal("unfinished deletion was acknowledged")
	}
	if _, e = New(context.Background(), f.main, f.authority, s.assertions, s.policy, s.root); e == nil {
		t.Fatal("restart replayed/ignored unfinished delete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.deleting) != 0 || len(s.deletingContent) != 0 || len(s.writes) != 0 {
		t.Fatal("failed deletion leaked runtime barrier")
	}
}
func TestDeleteSQLFailureLeavesFileRemovedButRowUnacknowledged(t *testing.T) {
	f, s, _, _, _, _, a := nativeImageFixture(t)
	id := str(get(a, "id"))
	path, e := s.file(id, str(get(a, "format")))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.main.Exec("CREATE TRIGGER abort_meme_delete BEFORE DELETE ON meme_assets BEGIN SELECT RAISE(ABORT,'synthetic_delete_fault'); END"); e != nil {
		t.Fatal(e)
	}
	operation, release, e := f.authority.OperationContext(context.Background(), f.main)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if ok, e := s.AdminDelete(operation, id); ok || e == nil {
		t.Fatal(ok, e)
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("bytes were not unlinked before SQL", e)
	}
	var count int
	if e = f.main.QueryRow("SELECT COUNT(*) FROM meme_assets WHERE id=?", id).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if _, e = s.AdminGet(context.Background(), id); e == nil {
		t.Fatal("SQL rollback acknowledged delete")
	}
}
