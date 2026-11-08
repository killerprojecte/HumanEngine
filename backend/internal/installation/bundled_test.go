//go:build bundled_sqlite_vec && linux && amd64

package installation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/vectorasset"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBundledInstallationLifecycle(t *testing.T) { testBundledInstallation(t) }
func TestBundledInstallationSourceFree(t *testing.T) {
	if os.Getenv("HUMANENGINE_SOURCE_FREE_STAGE") != "1" {
		t.Skip("run backend/tests/installation-source-free.sh")
	}
	for _, name := range []string{"node", "go"} {
		if path, e := exec.LookPath(name); e == nil {
			t.Fatal("unexpected development executable", path)
		}
	}
	for _, name := range []string{"src", "node_modules", "go.mod"} {
		if _, e := os.Stat(name); !os.IsNotExist(e) {
			t.Fatal("source-free directory contaminated", name, e)
		}
	}
	testBundledInstallation(t)
}
func testBundledInstallation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	extractions := filepath.Join(root, "extractions")
	if e := os.Mkdir(extractions, 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("TMPDIR", extractions)
	options := Options{Database: filepath.Join(root, "data", "main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"), SourceTimezone: "UTC"}
	for restart := 0; restart < 2; restart++ {
		s, e := OpenBundled(ctx, options)
		if e != nil {
			t.Fatal(e)
		}
		assets, err := os.ReadDir(extractions)
		if err != nil || len(assets) != 1 {
			t.Fatalf("extraction: %v %v", assets, err)
		}
		for name, want := range map[string]string{"LICENSE-MIT": vectorasset.License, "manifest.json": vectorasset.Manifest} {
			got, err := os.ReadFile(filepath.Join(extractions, assets[0].Name(), name))
			if err != nil || string(got) != want || len(got) == 0 {
				t.Fatalf("runtime attribution %s: %v", name, err)
			}
		}
		// The test never supplies an extension path or accesses fixture/source files.
		if s.VectorVersion != "v"+vectorasset.Version {
			t.Fatal("wrong real vector version", s.VectorVersion)
		}
		if _, e = s.DB.Exec("CREATE VIRTUAL TABLE IF NOT EXISTS bundled_vectors USING vec0(embedding float[3]);"); e != nil {
			t.Fatal(e)
		}
		if _, e = s.DB.Exec("INSERT INTO bundled_vectors(rowid,embedding) VALUES(?,?)", restart+1, "[1,2,3]"); e != nil {
			t.Fatal(e)
		}
		old, e := s.DB.Conn(ctx)
		if e != nil {
			t.Fatal(e)
		}
		e = old.Raw(func(any) error { return driver.ErrBadConn })
		if !errors.Is(e, driver.ErrBadConn) {
			t.Fatal(e)
		}
		if e = old.Close(); e != nil && !errors.Is(e, sql.ErrConnDone) {
			t.Fatal(e)
		}
		var length, fk, count int
		var journal, bucket string
		if e = s.DB.QueryRow("SELECT vec_length(embedding) FROM bundled_vectors WHERE rowid=?", restart+1).Scan(&length); e != nil || length != 3 {
			t.Fatal("replacement vector read", length, e)
		}
		if _, e = s.DB.Exec("UPDATE bundled_vectors SET embedding=? WHERE rowid=?", "[3,2,1]", restart+1); e != nil {
			t.Fatal(e)
		}
		if e = s.DB.QueryRow("SELECT count(*) FROM bundled_vectors").Scan(&count); e != nil || count != restart+1 {
			t.Fatal("restart persistence", count, e)
		}
		if e = s.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk); e != nil {
			t.Fatal(e)
		}
		if e = s.DB.QueryRow("PRAGMA journal_mode").Scan(&journal); e != nil {
			t.Fatal(e)
		}
		if e = s.DB.QueryRow("SELECT usage_bucket('2026-11-01T03:13:02.123Z','+05:45','hour')").Scan(&bucket); e != nil {
			t.Fatal(e)
		}
		if fk != 1 || journal != "wal" || bucket != "2026-11-01T02:15:00.000Z" {
			t.Fatal("replacement hook state", fk, journal, bucket)
		}
		gate, e := recovery.ReadGate(ctx, s.DB)
		if e != nil || gate.MinimumReader != 11 || gate.Head.Sequence != 1 {
			t.Fatal("automatic baseline/restart", gate, e)
		}
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
		entries, e := os.ReadDir(extractions)
		if e != nil || len(entries) != 0 {
			t.Fatal("extracted library not cleaned", entries, e)
		}
	}
	// Failure before startup must also clean this owner's extraction.
	bad := options
	bad.SourceTimezone = ""
	if s, e := OpenBundled(ctx, bad); e == nil {
		s.Close()
		t.Fatal("unconfigured parser accepted")
	}
	entries, e := os.ReadDir(extractions)
	if e != nil || len(entries) != 0 {
		t.Fatal("failed startup leaked extraction", entries, e)
	}
}
