//go:build linux

package installation

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
)

func leaseOptions(root string) Options {
	return Options{Database: filepath.Join(root, "data", "main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"), SourceTimezone: "UTC", VectorExtension: filepath.Join(root, "missing.so")}
}

// The helper uses the real Open boundary, not a launcher socket or SQL lock.
func TestWriterLeaseProcessHelper(t *testing.T) {
	mode := os.Getenv("HE_WRITER_LEASE_HELPER")
	if mode == "" {
		return
	}
	o := leaseOptions(os.Getenv("HE_WRITER_LEASE_ROOT"))
	if authority := os.Getenv("HE_WRITER_LEASE_AUTHORITY"); authority != "" {
		o.AuthorityDirectory = authority
	}
	switch mode {
	case "hold-new":
		if err := os.MkdirAll(filepath.Dir(o.Database), 0700); err != nil {
			t.Fatal(err)
		}
		l, err := acquireDatabaseLease(o.Database)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		fmt.Println("LEASE_READY")
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "busy":
		s, err := Open(context.Background(), o)
		if s != nil {
			s.Close()
			t.Fatal("second writer opened")
		}
		if !errors.Is(err, ErrWriterBusy) || err.Error() != ErrWriterBusy.Error() {
			t.Fatalf("expected sanitized busy error: %v", err)
		}
	default:
		t.Fatal("unknown helper mode")
	}
}

func leaseChild(t *testing.T, root, mode, authority string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWriterLeaseProcessHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "HE_WRITER_LEASE_HELPER="+mode, "HE_WRITER_LEASE_ROOT="+root, "HE_WRITER_LEASE_AUTHORITY="+authority)
	return cmd
}
func assertChildBusy(t *testing.T, root, authority string) {
	t.Helper()
	if out, err := leaseChild(t, root, "busy", authority).CombinedOutput(); err != nil {
		t.Fatalf("second process: %v\n%s", err, out)
	}
}
func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected side effect at %s: %v", path, err)
	}
}
func TestWriterLeaseNewDatabaseDifferentAuthorityProcess(t *testing.T) {
	root := t.TempDir()
	o := leaseOptions(root)
	cmd := leaseChild(t, root, "hold-new", "")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		stdin.Close()
		if !waited {
			cmd.Wait()
		}
	}()
	scan := bufio.NewScanner(stdout)
	ready := false
	for scan.Scan() {
		if scan.Text() == "LEASE_READY" {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("child did not acquire new database lease", scan.Err())
	}
	other := filepath.Join(root, "other-authority")
	assertChildBusy(t, root, other)
	assertMissing(t, o.AuthorityDirectory)
	assertMissing(t, other)
	assertMissing(t, filepath.Join(filepath.Dir(o.Database), ".secrets.key"))
	before, err := os.Stat(o.Database)
	if err != nil || before.Size() != 0 {
		t.Fatal("new database mutated", err)
	}
	stdin.Close()
	_, _ = io.Copy(io.Discard, stdout)
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	waited = true
	l, err := acquireDatabaseLease(o.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(o.Database)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("lease release replaced/unlinked database", err)
	}
}
func TestWriterLeaseCrashReleasesKernelOwnership(t *testing.T) {
	root := t.TempDir()
	cmd := leaseChild(t, root, "hold-new", "")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	scan := bufio.NewScanner(stdout)
	ready := false
	for scan.Scan() {
		if scan.Text() == "LEASE_READY" {
			ready = true
			break
		}
	}
	if !ready {
		cmd.Wait()
		t.Fatal("no ready handshake")
	}
	assertChildBusy(t, root, "")
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("crash helper exited cleanly")
	}
	l, err := acquireDatabaseLease(leaseOptions(root).Database)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
}
func TestWriterLeasePathAliasesAndStableAuthority(t *testing.T) {
	root := t.TempDir()
	o := leaseOptions(root)
	if err := os.MkdirAll(filepath.Dir(o.Database), 0700); err != nil {
		t.Fatal(err)
	}
	l, err := acquireDatabaseLease(o.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err = acquireDatabaseLease(filepath.Dir(o.Database) + "/./main.sqlite"); !errors.Is(err, ErrWriterBusy) {
		t.Fatal("dot alias", err)
	}
	alias := filepath.Join(root, "alias")
	if err = os.Symlink(filepath.Dir(o.Database), alias); err != nil {
		t.Fatal(err)
	}
	if _, err = acquireDatabaseLease(filepath.Join(alias, "main.sqlite")); !errors.Is(err, errWriterLeasePath) {
		t.Fatal("ancestor symlink", err)
	}
	hard := filepath.Join(root, "hard.sqlite")
	if err = os.Link(o.Database, hard); err != nil {
		t.Fatal(err)
	}
	if _, err = acquireDatabaseLease(hard); !errors.Is(err, errWriterLeasePath) {
		t.Fatal("hardlink", err)
	}
	if _, err = acquireDatabaseLease(o.Database); !errors.Is(err, errWriterLeasePath) {
		t.Fatal("original hardlinked path", err)
	}
	if _, err = l.database.WriteAt([]byte("synthetic"), 0); err != nil {
		t.Fatal("hardlink rejection disturbed first holder", err)
	}
	if err = l.acquireAuthority(o.AuthorityDirectory); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(o.AuthorityDirectory, ".writer.lock")
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	second := &writerLease{}
	if err = second.acquireAuthority(o.AuthorityDirectory); !errors.Is(err, ErrWriterBusy) {
		t.Fatal("authority not exclusive", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if err = second.acquireAuthority(o.AuthorityDirectory); err != nil {
		t.Fatal(err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(lock)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("authority lock inode replaced", err)
	}
}
func TestWriterLeaseOpenFailureReleasesLast(t *testing.T) {
	root := t.TempDir()
	o := leaseOptions(root)
	// Invalid timezone fails preflight before reserving a database.
	bad := o
	bad.SourceTimezone = "Not/A/Timezone"
	if s, err := Open(context.Background(), bad); s != nil || err == nil {
		t.Fatal("bad preflight accepted")
	}
	assertMissing(t, o.Database)
	// Missing native extension fails after enrollment and secret setup. Repeated
	// failure must preserve identity/quarantine semantics without leaking a lease.
	for i := 0; i < 2; i++ {
		s, err := Open(context.Background(), o)
		if s != nil || err == nil || errors.Is(err, ErrWriterBusy) || !strings.Contains(err.Error(), "sqlite-vec") {
			t.Fatalf("partial startup %d: %v", i, err)
		}
		l, e := acquireDatabaseLease(o.Database)
		if e != nil {
			t.Fatal(e)
		}
		if e = l.acquireAuthority(o.AuthorityDirectory); e != nil {
			l.Close()
			t.Fatal(e)
		}
		if e = l.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
func TestWriterLeaseLiveStoreProcessReadersAndReopen(t *testing.T) {
	vector := os.Getenv("HUMANENGINE_TEST_SQLITE_VEC")
	if vector == "" {
		vector = "../../../node_modules/sqlite-vec-linux-x64/vec0.so"
	}
	vector, err := filepath.Abs(vector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(vector); err != nil {
		t.Fatal("required real vector fixture unavailable", err)
	}
	root := t.TempDir()
	o := leaseOptions(root)
	o.VectorExtension = vector
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	head, err := s.Authority.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(filepath.Dir(o.Database), ".secrets.key")
	if _, err = credentials.LoadKey(key, false); err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "wrong-authority")
	assertChildBusy(t, root, other)
	assertMissing(t, other)
	// A distinct database targeting the active authority must also be refused
	// before enrollment. This exercises the second lease across processes.
	competing := t.TempDir()
	assertChildBusy(t, competing, o.AuthorityDirectory)
	competingDB, e := storage.OpenExisting(leaseOptions(competing).Database, false)
	if e != nil {
		t.Fatal(e)
	}
	var tables int
	e = competingDB.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables)
	e = errors.Join(e, competingDB.Close())
	if e != nil || tables != 0 {
		t.Fatal("authority contender enrolled or migrated", e, tables)
	}
	assertMissing(t, filepath.Join(filepath.Dir(leaseOptions(competing).Database), ".secrets.key"))

	keyAfter, err := os.ReadFile(key)
	if err != nil || sha256.Sum256(keyBefore) != sha256.Sum256(keyAfter) {
		t.Fatal("second writer touched secrets", err)
	}
	got, err := s.Authority.Head(context.Background())
	if err != nil || got != head {
		t.Fatal("second writer changed authority", err)
	}
	for i := 0; i < 2; i++ {
		reader, e := storage.OpenExisting(o.Database, false)
		if e != nil {
			t.Fatal(e)
		}
		defer reader.Close()
		var count int
		if e = reader.QueryRow("SELECT count(*) FROM recovery_gate").Scan(&count); e != nil || count != 1 {
			t.Fatal("concurrent reader", e)
		}
	}
	if _, err = s.DB.Exec("CREATE TABLE writer_lease_probe(value INTEGER); INSERT INTO writer_lease_probe VALUES(1)"); err != nil {
		t.Fatal("first writer unusable", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// Explicit authority is ignored for an already enrolled installation.
	o.AuthorityDirectory = other
	reopened, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, other)
}

func TestWriterLeaseRejectsSymlinkLockWithoutMutation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	authority := filepath.Join(root, "authority")
	if err := os.Mkdir(authority, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(authority, ".writer.lock")); err != nil {
		t.Fatal(err)
	}
	l := &writerLease{}
	if err := l.acquireAuthority(authority); !errors.Is(err, errWriterLeasePath) {
		t.Fatal(err)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "untouched" {
		t.Fatal("symlink target changed", err)
	}
}

// Root/CAP_DAC_OVERRIDE can hide an O_RDONLY regression. This acceptance test
// fails rather than presenting a privileged pass as real permission coverage.
func TestWriterLeaseSearchOnlyAncestorNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("permission regression requires a non-root native test runner")
	}
	root := t.TempDir()
	ancestor := filepath.Join(root, "search-only")
	child := filepath.Join(ancestor, "writable-child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, 0300); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ancestor, 0700)
	if entries, err := os.ReadDir(ancestor); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("fixture did not enforce non-readable ancestor: entries=%d err=%v", len(entries), err)
	}
	// Both DB and independent authority resolve through the non-readable ancestor.
	o := leaseOptions(child)
	if _, err := recovery.AuthorityPath(o.Database, o.AuthorityDirectory); err != nil {
		t.Fatal("existing independent-path contract", err)
	}
	if err := os.MkdirAll(filepath.Dir(o.Database), 0700); err != nil {
		t.Fatal(err)
	}
	l, err := acquireDatabaseLease(o.Database)
	if err != nil {
		t.Fatal("database search-only ancestor", err)
	}
	defer l.Close()
	if err = l.acquireAuthority(o.AuthorityDirectory); err != nil {
		t.Fatal("authority search-only ancestor", err)
	}
	if _, err = acquireDatabaseLease(o.Database); !errors.Is(err, ErrWriterBusy) {
		t.Fatal("search-only path lost exclusion", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := acquireDatabaseLease(o.Database)
	if err != nil {
		t.Fatal("search-only reopen", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterLeaseRejectedGateOnlyPreparesStableLock(t *testing.T) {
	for _, kind := range []string{"legacy", "quarantine", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			o := leaseOptions(root)
			if err := os.MkdirAll(filepath.Dir(o.Database), 0700); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite3", o.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec("CREATE TABLE synthetic_existing(value TEXT); INSERT INTO synthetic_existing VALUES('unchanged')"); err != nil {
				t.Fatal(err)
			}
			raw := ""
			if kind != "legacy" {
				g := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: o.AuthorityDirectory, MinimumReader: 1, State: "quarantine"}
				if kind == "unsupported" {
					g.State = "ready"
					g.MinimumReader = recovery.ReaderCapability + 1
				}
				encoded, e := json.Marshal(g)
				if e != nil {
					t.Fatal(e)
				}
				raw = string(encoded)
				if _, err = db.Exec("CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY, value TEXT); INSERT INTO recovery_gate VALUES(1,?)", raw); err != nil {
					t.Fatal(err)
				}
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(o.Database)
			if err != nil {
				t.Fatal(err)
			}
			assertMissing(t, o.AuthorityDirectory)
			store, err := Open(context.Background(), o)
			if store != nil {
				store.Close()
				t.Fatal("rejected installation opened")
			}
			want := "recovery_quarantined_or_unsupported"
			if kind == "legacy" {
				want = "recovery_legacy_enrollment_required"
			}
			if err == nil || err.Error() != want {
				t.Fatalf("gate rejection changed: %v", err)
			}
			after, err := os.ReadFile(o.Database)
			if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("rejected open changed database bytes/schema/gate", err)
			}
			entries, err := os.ReadDir(o.AuthorityDirectory)
			if err != nil || len(entries) != 1 || entries[0].Name() != ".writer.lock" {
				t.Fatal("rejected open did more than prepare stable lock", entries, err)
			}
			assertMissing(t, filepath.Join(filepath.Dir(o.Database), ".secrets.key"))
			probe, err := storage.OpenExisting(o.Database, false)
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Close()
			var gates, secrets int
			if err = probe.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='recovery_gate'").Scan(&gates); err != nil {
				t.Fatal(err)
			}
			if err = probe.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='secrets'").Scan(&secrets); err != nil || secrets != 0 {
				t.Fatal("rejected open created secret schema", err)
			}
			if kind == "legacy" {
				if gates != 0 {
					t.Fatal("legacy gate repaired")
				}
			} else {
				var got string
				if err = probe.QueryRow("SELECT value FROM recovery_gate WHERE id=1").Scan(&got); err != nil || got != raw {
					t.Fatal("quarantined/unsupported gate rewritten", err)
				}
			}
			l, err := acquireDatabaseLease(o.Database)
			if err != nil {
				t.Fatal("rejected open leaked DB lease", err)
			}
			defer l.Close()
			if err = l.acquireAuthority(o.AuthorityDirectory); err != nil {
				t.Fatal("rejected open leaked authority lease", err)
			}
		})
	}
}
