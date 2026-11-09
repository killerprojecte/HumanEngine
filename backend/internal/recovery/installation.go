package recovery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"os"
	"path/filepath"
	"strings"
)

// AuthorityPath preserves ledger.ts's independent-directory/ancestor-symlink policy.
func AuthorityPath(database, explicit string) (string, error) {
	db, err := filepath.Abs(database)
	if err != nil {
		return "", err
	}
	data := filepath.Dir(db)
	if explicit == "" {
		if filepath.Dir(data) == data {
			return "", errors.New("recovery_authority_path_required")
		}
		sum := sha256.Sum256([]byte(db))
		explicit = filepath.Join(filepath.Dir(data), ".humanengine-authority", hex.EncodeToString(sum[:]))
	}
	path, err := filepath.Abs(explicit)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(data, path)
	if err != nil {
		return "", err
	}
	// Source deliberately uses startsWith('..'), including names like '..other'.
	if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
		return "", errors.New("recovery_authority_not_independent")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("recovery_symlink_denied")
		}
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return path, nil
}
func installationUUID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func installationGate(ctx context.Context, q Query) (*Gate, error) {
	var exists bool
	if e := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM main.sqlite_master WHERE type='table' AND name='recovery_gate')").Scan(&exists); e != nil {
		return nil, e
	}
	if !exists {
		return nil, nil
	}
	var raw string
	if e := q.QueryRowContext(ctx, "SELECT value FROM main.recovery_gate WHERE id=1").Scan(&raw); e != nil {
		return nil, errors.New("recovery_gate_invalid")
	}
	g, _, e := decodeGate(raw)
	return &g, e
}

// Enroll is an explicit operator trust decision, never repair of damaged gates.
func Enroll(ctx context.Context, main *sql.DB, database, explicit string) (*Authority, error) {
	return enrollInstallation(ctx, main, database, explicit, nil)
}
func enrollInstallation(ctx context.Context, main *sql.DB, database, explicit string, afterReserve func() error) (_ *Authority, err error) {
	c, err := main.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err = beginOwned(ctx, c, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rollbackOwned(c)) }()
	g, err := installationGate(ctx, c)
	if err != nil {
		return nil, err
	}
	if g != nil {
		return nil, errors.New("recovery_already_enrolled")
	}
	path, err := AuthorityPath(database, explicit)
	if err != nil {
		return nil, err
	}
	file := filepath.Join(path, "authority.sqlite")
	if _, e := os.Lstat(file); e == nil {
		return nil, errors.New("recovery_existing_authority_requires_reconciliation")
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	// First reversible main write MUST precede external reservation, even readonly.
	if _, err = c.ExecContext(ctx, "CREATE TABLE main.recovery_gate (id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)"); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil, errors.New("recovery_existing_authority_requires_reconciliation")
	}
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	// Every subsequent error retains the side file as a reconciliation obligation.
	if afterReserve != nil {
		if err = afterReserve(); err != nil {
			return nil, err
		}
	}
	instance, err := installationUUID()
	if err != nil {
		return nil, err
	}
	pool, err := storage.OpenExisting(file, true)
	if err != nil {
		return nil, err
	}
	_, err = pool.ExecContext(ctx, "PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL; CREATE TABLE identity (id TEXT PRIMARY KEY, format INTEGER NOT NULL); CREATE TABLE revocations (sequence INTEGER PRIMARY KEY, previous_hash TEXT NOT NULL, payload TEXT NOT NULL, hash TEXT NOT NULL)")
	if err == nil {
		_, err = pool.ExecContext(ctx, "INSERT INTO identity VALUES(?,1)", instance)
	}
	err = errors.Join(err, pool.Close())
	if err != nil {
		return nil, err
	}
	a, err := OpenWriter(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = a.Close()
		}
	}()
	h, err := a.Head(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(Gate{Head: h, Authority: path, MinimumReader: 1, State: "ready"})
	if err != nil {
		return nil, err
	}
	if _, err = c.ExecContext(ctx, "INSERT INTO main.recovery_gate VALUES(1,?)", string(raw)); err != nil {
		return nil, err
	}
	if err = commitOwned(ctx, c, "main", nil); err != nil {
		return nil, err
	}
	return a, nil
}
func OpenInstallationGate(ctx context.Context, main *sql.DB, database, explicit string) (*Authority, error) {
	g, err := installationGate(ctx, main)
	if err != nil {
		return nil, err
	}
	if g == nil {
		var populated bool
		if err = main.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM main.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%')").Scan(&populated); err != nil {
			return nil, err
		}
		if populated {
			return nil, errors.New("recovery_legacy_enrollment_required")
		}
		return Enroll(ctx, main, database, explicit)
	}
	if g.State != "ready" || g.MinimumReader > ReaderCapability {
		return nil, errors.New("recovery_quarantined_or_unsupported")
	}
	path, err := AuthorityPath(database, g.Authority)
	if err != nil {
		return nil, err
	}
	a, err := OpenWriter(ctx, path)
	if err != nil {
		return nil, err
	}
	h, err := a.Head(ctx)
	if err == nil && h != g.Head {
		err = errors.New("recovery_authority_head_changed")
	}
	if err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}

// InstallationAuthorityPath discovers the durable gate authority without writing.
// The installation owner must hold its database lease while discovering and
// opening the gate, so NEW enrollment cannot race with another runtime owner.
func InstallationAuthorityPath(ctx context.Context, main *sql.DB, database, explicit string) (string, error) {
	g, err := installationGate(ctx, main)
	if err != nil {
		return "", err
	}
	if g != nil {
		explicit = g.Authority
	}
	return AuthorityPath(database, explicit)
}
