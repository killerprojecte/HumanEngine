package recovery

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func installationFixture(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	if e := storage.RequireFTS5(); e != nil {
		t.Skip(e)
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if e := os.MkdirAll(data, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(data, "main.sqlite")
	db, e := sql.Open("sqlite3", path+"?_busy_timeout=5000")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if e = db.Ping(); e != nil {
		t.Fatal(e)
	}
	return db, path, filepath.Join(root, "authority")
}
func TestInstallationFreshRestartAndGateValidation(t *testing.T) {
	db, path, dir := installationFixture(t)
	ctx := context.Background()
	a, e := OpenInstallationGate(ctx, db, path, dir)
	if e != nil {
		t.Fatal(e)
	}
	h, e := a.Head(ctx)
	if e != nil {
		t.Fatal(e)
	}
	a.Close()
	if h.Sequence != 0 || h.Hash != strings.Repeat("0", 64) {
		t.Fatal(h)
	}
	a, e = OpenInstallationGate(ctx, db, path, dir)
	if e != nil {
		t.Fatal(e)
	}
	a.Close()
	if _, e = db.Exec("UPDATE recovery_gate SET value=json_set(value,'$.head.sequence',1)"); e != nil {
		t.Fatal(e)
	}
	if a, e = OpenInstallationGate(ctx, db, path, dir); e == nil {
		a.Close()
		t.Fatal("mismatched gate accepted")
	}
	var seq int
	db.QueryRow("SELECT json_extract(value,'$.head.sequence') FROM recovery_gate").Scan(&seq)
	if seq != 1 {
		t.Fatal("gate repaired")
	}
}
func TestInstallationLegacyExplicitAndDamagedGate(t *testing.T) {
	db, path, dir := installationFixture(t)
	ctx := context.Background()
	db.Exec("CREATE TABLE legacy(value TEXT)")
	if _, e := OpenInstallationGate(ctx, db, path, dir); e == nil || !strings.Contains(e.Error(), "legacy_enrollment_required") {
		t.Fatal(e)
	}
	a, e := Enroll(ctx, db, path, dir)
	if e != nil {
		t.Fatal(e)
	}
	a.Close()
	db.Exec("DELETE FROM recovery_gate")
	if _, e = OpenInstallationGate(ctx, db, path, dir); e == nil || !strings.Contains(e.Error(), "gate_invalid") {
		t.Fatal(e)
	}
}
func TestInstallationReadonlyBeforeReservation(t *testing.T) {
	db, path, dir := installationFixture(t)
	db.Close()
	ro, e := storage.OpenExisting(path, false)
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	if _, e = Enroll(context.Background(), ro, path, dir); e == nil {
		t.Fatal("readonly enrolled")
	}
	if _, e = os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("external directory created", e)
	}
}
func TestInstallationLateFailureRetainsReservation(t *testing.T) {
	db, path, dir := installationFixture(t)
	marker := errors.New("synthetic late failure")
	_, e := enrollInstallation(context.Background(), db, path, dir, func() error { return marker })
	if !errors.Is(e, marker) {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dir, "authority.sqlite")); e != nil {
		t.Fatal(e)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='recovery_gate'").Scan(&n)
	if n != 0 {
		t.Fatal("DDL not rolled back")
	}
	if _, e = Enroll(context.Background(), db, path, dir); e == nil || !strings.Contains(e.Error(), "requires_reconciliation") {
		t.Fatal(e)
	}
}
func TestInstallationCompetingCreators(t *testing.T) {
	db, path, dir := installationFixture(t)
	second, e := storage.OpenExisting(path, true)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, main := range []*sql.DB{db, second} {
		wg.Add(1)
		go func(main *sql.DB) {
			defer wg.Done()
			a, e := Enroll(context.Background(), main, path, dir)
			if e == nil {
				a.Close()
			}
			results <- e
		}(main)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !strings.Contains(e.Error(), "already_enrolled") {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
}
func TestInstallationPaths(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "data", "main.sqlite")
	if _, e := AuthorityPath(db, filepath.Join(root, "data", "authority")); e == nil {
		t.Fatal("nested authority")
	}
	if e := os.Mkdir(filepath.Join(root, "real"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); e != nil {
		t.Fatal(e)
	}
	if _, e := AuthorityPath(db, filepath.Join(root, "alias", "authority")); e == nil {
		t.Fatal("symlink authority")
	}
	if _, e := AuthorityPath("/main.sqlite", ""); e == nil {
		t.Fatal("root default accepted")
	}
}
