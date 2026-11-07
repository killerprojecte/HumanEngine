package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"github.com/mattn/go-sqlite3"
)

type writerFixture struct {
	authority      *Authority
	main, external *sql.DB
	gate           Gate
	mainPath       string
}

func newWriterFixture(t *testing.T) writerFixture {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	data := filepath.Join(root, "live")
	for _, dir := range []string{path, data} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	external, err := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	external.SetMaxOpenConns(1)
	_, err = external.Exec(`CREATE TABLE identity (id TEXT PRIMARY KEY, format INTEGER NOT NULL); CREATE TABLE revocations (sequence INTEGER PRIMARY KEY, previous_hash TEXT NOT NULL, payload TEXT NOT NULL, hash TEXT NOT NULL); INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(data, "main.sqlite")
	main, err := sql.Open("sqlite3", mainPath)
	if err != nil {
		t.Fatal(err)
	}
	main.SetMaxOpenConns(1)
	if _, err = main.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL); CREATE TABLE data(value TEXT); INSERT INTO data VALUES('original')`); err != nil {
		t.Fatal(err)
	}
	gate := Gate{Head: Head{"12345678-1234-4123-8123-123456789012", 0, strings.Repeat("0", 64)}, Authority: path, MinimumReader: 1, State: "ready"}
	save(t, main, gate)
	authority, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(path, "authority.sqlite"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("writable authority permissions", info, err)
	}
	t.Cleanup(func() { authority.Close(); main.Close(); external.Close() })
	return writerFixture{authority, main, external, gate, mainPath}
}
func noMutation(*MainMutation) error { return nil }
func publishValue(a *Authority, main *sql.DB) (value string, err error) {
	err = a.Publish(context.Background(), main, func(conn *sql.Conn) ([]byte, error) {
		var v string
		err := conn.QueryRowContext(context.Background(), "SELECT value FROM main.data").Scan(&v)
		return []byte(v), err
	}, func(b []byte) error { value = string(b); return nil })
	return
}
func assertMain(t *testing.T, f writerFixture, want string, sequence int64, reader int) {
	t.Helper()
	var value string
	if err := f.main.QueryRow("SELECT value FROM data").Scan(&value); err != nil {
		t.Fatal(err)
	}
	gate, err := ReadGate(context.Background(), f.main)
	if err != nil {
		t.Fatal(err)
	}
	if value != want || gate.Head.Sequence != sequence || gate.MinimumReader != reader {
		t.Fatalf("main=%q gate=%+v", value, gate)
	}
}
func assertRestartGap(t *testing.T, f writerFixture) {
	t.Helper()
	reader, err := Open(context.Background(), f.gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	value, err := publishValue(reader, f.main)
	if err == nil || !strings.Contains(err.Error(), "recovery_authority_head_changed") || value != "" {
		t.Fatalf("gap publication %q: %v", value, err)
	}
	called := false
	_, err = f.authority.Write(context.Background(), f.main, []Revocation{Source{"s", "later"}}, 1, func(*MainMutation) error { called = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "recovery_authority_head_changed") || called {
		t.Fatalf("gap write: called=%v err=%v", called, err)
	}
}
func TestAppendCommitsExactHashDedupAndControlledCache(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	head := f.gate.Head
	for i := 0; i < 32; i++ {
		previous := head
		input := Source{"s", fmt.Sprintf("m%d", i)}
		next, err := f.authority.Append(ctx, input, head, false)
		if err != nil {
			t.Fatal(err)
		}
		head = next
		payload := fmt.Sprintf(`{"kind":"source","sceneId":"s","messageId":"m%d"}`, i)
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", head.InstanceID, head.Sequence, previous.Hash, payload)))
		if head.Hash != hex.EncodeToString(sum[:]) {
			t.Fatal("hash/payload incompatibility")
		}
		duplicate, err := f.authority.Append(ctx, input, head, false)
		if err != nil || duplicate != head {
			t.Fatal("dedup", duplicate, err)
		}
	}
	if f.authority.Scans() != 1 {
		t.Fatalf("own appends rescanned history: %d", f.authority.Scans())
	}
	head, err := f.authority.Append(ctx, Source{"s", "m31"}, head, true)
	if err != nil || head.Sequence != 33 {
		t.Fatal("force", head, err)
	}
	if f.authority.Scans() != 1 {
		t.Fatal("forced append rescanned history")
	}
	if _, err = f.external.Exec(`UPDATE revocations SET payload='{"kind":"source","sceneId":"s","messageId":"tampered"}' WHERE sequence=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.authority.Append(ctx, Source{"s", "next"}, head, false); err == nil || !strings.Contains(err.Error(), "chain_invalid") {
		t.Fatal("historical external mutation admitted", err)
	}
	if f.authority.Scans() != 2 {
		t.Fatal("external mutation was not audited")
	}
}
func TestTypedRepeatedDomainsAlwaysAppend(t *testing.T) {
	hash := strings.Repeat("a", 64)
	config := map[string]bool{}
	for key, since := range configurationIntroduced {
		if since == 1 {
			config[key] = false
		}
	}
	cases := []Revocation{
		AssertionEligibility{OperationID: "12345678-1234-4123-8123-123456789012", Mode: "baseline"},
		ConfigurationFlags{SchemaVersion: 1, Flags: config},
		SceneFlags{SchemaVersion: 1, SceneID: "s", IdentityHash: hash, Flags: map[string]bool{"participationEnabled": false, "learningEnabled": false, "proactiveEnabled": false}},
		MemeState{ID: hash, ContentHash: hash, QualificationHash: hash},
		ExperienceState{SceneID: "s", ID: "e", QualificationHash: hash, Version: 1, Status: "retired", Reason: "review_retired"},
	}
	f := newWriterFixture(t)
	head := f.gate.Head
	for _, event := range cases {
		for range 2 {
			var err error
			head, err = f.authority.Append(context.Background(), event, head, false)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if head.Sequence != 10 || f.authority.Scans() != 1 {
		t.Fatal(head, f.authority.Scans())
	}
}
func TestWriteDurableIntentBeforeMainACKAndCapabilityUpgrade(t *testing.T) {
	f := newWriterFixture(t)
	observer, err := Open(context.Background(), f.gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	otherMain, err := storage.OpenExisting(f.mainPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer otherMain.Close()
	seenGap := false
	head, err := f.authority.write(context.Background(), f.main, []Revocation{Source{"s", "m"}}, 2, func(tx *MainMutation) error {
		var count int
		if err := f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("intent not durable: %d", count)
		}
		gate, err := ReadGate(context.Background(), otherMain)
		if err != nil {
			return err
		}
		if gate.Head.Sequence != 0 || gate.MinimumReader != 1 {
			return errors.New("main prematurely acknowledged")
		}
		_, err = tx.Exec("UPDATE data SET value='changed'")
		return err
	}, writeHooks{afterIntent: func(Head) error {
		_, err := publishValue(observer, otherMain)
		seenGap = err != nil && strings.Contains(err.Error(), "recovery_authority_head_changed")
		if !seenGap {
			return fmt.Errorf("intent/main gap not rejected: %v", err)
		}
		return nil
	}})
	if err != nil || head.Sequence != 1 || !seenGap {
		t.Fatal(head, err)
	}
	assertMain(t, f, "changed", 1, 2)
	for _, level := range []int{5, 11, 2} {
		if _, err = f.authority.Write(context.Background(), f.main, nil, level, noMutation); err != nil {
			t.Fatal(err)
		}
	}
	assertMain(t, f, "changed", 1, 11)
	value, err := publishValue(observer, otherMain)
	if err != nil || value != "changed" {
		t.Fatal(value, err)
	}
	var format int
	if err = f.external.QueryRow("SELECT format FROM identity").Scan(&format); err != nil || format != 1 {
		t.Fatal(format, err)
	}
	if f.authority.Scans() != 1 {
		t.Fatal("write/ACK cycle unnecessarily rescanned history")
	}
}
func TestWriteFaultBoundariesPreserveIntentAndRollbackMain(t *testing.T) {
	for _, point := range []string{"after_intent", "mutation", "before_main_commit", "main_commit_failed", "authority_commit_failed", "authority_commit_unknown", "main_commit_unknown", "cancel_after_intent", "cancel_before_main_commit"} {
		t.Run(point, func(t *testing.T) {
			f := newWriterFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			injected := errors.New("injected failure")
			hooks := writeHooks{}
			apply := func(tx *MainMutation) error {
				_, err := tx.Exec("UPDATE data SET value='changed'")
				if err == nil && point == "mutation" {
					return injected
				}
				return err
			}
			switch point {
			case "after_intent":
				hooks.afterIntent = func(Head) error { return injected }
			case "before_main_commit":
				hooks.beforeMainCommit = func() error { return injected }
			case "cancel_after_intent":
				hooks.afterIntent = func(Head) error { cancel(); return nil }
			case "cancel_before_main_commit":
				hooks.beforeMainCommit = func() error { cancel(); return nil }
			default:
				hooks.commit = func(ctx context.Context, conn *sql.Conn, database string) error {
					if point == database+"_commit_failed" {
						return injected
					}
					if point == database+"_commit_unknown" {
						if err := execCommit(ctx, conn, database); err != nil {
							return err
						}
						cancel()
						return context.Canceled
					}
					return execCommit(ctx, conn, database)
				}
			}
			_, err := f.authority.write(ctx, f.main, []Revocation{Source{"s", "m"}}, 11, apply, hooks)
			if err == nil {
				t.Fatal("fault ignored")
			}
			if strings.HasSuffix(point, "unknown") && f.authority.cached {
				t.Fatal("uncertain commit retained cache")
			}
			if strings.HasSuffix(point, "unknown") && !errors.Is(err, ErrCommitOutcomeUnknown) {
				t.Fatal("commit uncertainty lost", err)
			}
			if strings.HasPrefix(point, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
			count := 0
			if err = f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if point == "authority_commit_failed" {
				if count != 0 {
					t.Fatal("failed authority commit persisted")
				}
				assertMain(t, f, "original", 0, 1)
				if _, err = f.authority.Write(context.Background(), f.main, []Revocation{Source{"s", "retry"}}, 1, noMutation); err != nil {
					t.Fatal("known rollback blocked retry", err)
				}
			} else if point == "main_commit_unknown" {
				assertMain(t, f, "changed", 1, 11)
				if value, err := publishValue(f.authority, f.main); err != nil || value != "changed" {
					t.Fatal("committed main not discoverable", value, err)
				}
			} else {
				if count != 1 {
					t.Fatal("durable authority lost", count)
				}
				assertMain(t, f, "original", 0, 1)
				assertRestartGap(t, f)
			}
			active, err := transactionActive(f.authority.conn)
			if err != nil || active {
				t.Fatal("authority transaction leaked", active, err)
			}
		})
	}
}
func TestExternalAppendBetweenIntentAndACKRefusesMain(t *testing.T) {
	f := newWriterFixture(t)
	other, err := OpenWriter(context.Background(), f.gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	called := false
	_, err = f.authority.write(context.Background(), f.main, []Revocation{Source{"s", "one"}}, 1, func(*MainMutation) error { called = true; return nil }, writeHooks{afterIntent: func(h Head) error {
		_, err := other.Append(context.Background(), Source{"s", "external"}, h, false)
		return err
	}})
	if err == nil || !strings.Contains(err.Error(), "recovery_authority_head_changed") || called {
		t.Fatal("stale ACK admitted", called, err)
	}
	assertMain(t, f, "original", 0, 1)
	assertRestartGap(t, f)
}
func TestAuthorityBusyCommitRollsBackWithoutDedupPoison(t *testing.T) {
	f := newWriterFixture(t)
	if _, err := f.authority.conn.ExecContext(context.Background(), "PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.external.Exec("BEGIN; SELECT id FROM identity"); err != nil {
		t.Fatal(err)
	}
	_, err := f.authority.Append(context.Background(), Source{"s", "retry"}, f.gate.Head, false)
	if err == nil || errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatal("real busy COMMIT outcome", err)
	}
	if active, err := transactionActive(f.authority.conn); err != nil || active {
		t.Fatal("failed COMMIT left transaction", active, err)
	}
	if _, err = f.external.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	head, err := f.authority.Append(context.Background(), Source{"s", "retry"}, f.gate.Head, false)
	if err != nil || head.Sequence != 1 {
		t.Fatal("rolled back intent poisoned dedup", head, err)
	}
}
func TestAuthorityTriggerMutationForcesAudit(t *testing.T) {
	f := newWriterFixture(t)
	head, err := f.authority.Append(context.Background(), Source{"s", "first"}, f.gate.Head, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.external.Exec(`CREATE TRIGGER corrupt_history AFTER INSERT ON revocations BEGIN UPDATE revocations SET hash='broken' WHERE sequence=1; END`); err != nil {
		t.Fatal(err)
	}
	_, err = f.authority.Append(context.Background(), Source{"s", "second"}, head, false)
	if err == nil || !strings.Contains(err.Error(), "chain_invalid") {
		t.Fatal("trigger mutation escaped", err)
	}
	var count int
	if err = f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err = f.external.Exec("DROP TRIGGER corrupt_history"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.authority.Append(context.Background(), Source{"s", "second"}, head, false); err != nil {
		t.Fatal(err)
	}
}
func TestMutationCapabilityCannotCommitOrForgeACK(t *testing.T) {
	for _, query := range []string{"COMMIT", "ROLLBACK", "SAVEPOINT bypass", "PRAGMA synchronous=OFF", "UPDATE recovery_gate SET value='forged'", "CREATE TABLE bypass(value TEXT)"} {
		t.Run(query, func(t *testing.T) {
			f := newWriterFixture(t)
			_, err := f.authority.Write(context.Background(), f.main, []Revocation{Source{"s", "m"}}, 11, func(tx *MainMutation) error {
				if _, err := tx.Exec("UPDATE data SET value='changed'"); err != nil {
					return err
				}
				_, err := tx.Exec(query)
				return err
			})
			if err == nil {
				t.Fatal("forbidden SQL accepted")
			}
			assertMain(t, f, "original", 0, 1)
			assertRestartGap(t, f)
		})
	}
	f := newWriterFixture(t)
	var retained *MainMutation
	if _, err := f.authority.Write(context.Background(), f.main, nil, 1, func(tx *MainMutation) error { retained = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := retained.Exec("UPDATE data SET value='later'"); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("retained capability usable", err)
	}
	assertMain(t, f, "original", 0, 1)
}
func TestMutationPanicRollsBackAndRetiresCapability(t *testing.T) {
	f := newWriterFixture(t)
	var retained *MainMutation
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic disappeared")
			}
		}()
		_, _ = f.authority.Write(context.Background(), f.main, []Revocation{Source{"s", "m"}}, 11, func(tx *MainMutation) error {
			retained = tx
			_, _ = tx.Exec("UPDATE data SET value='changed'")
			panic("injected")
		})
	}()
	if _, err := retained.Exec("COMMIT"); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal(err)
	}
	assertMain(t, f, "original", 0, 1)
	assertRestartGap(t, f)
}
func TestMainWriterWaitDoesNotHoldAuthorityAndCancellationCleansUp(t *testing.T) {
	f := newWriterFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocker, err := sql.Open("sqlite3", f.mainPath)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err = blocker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	conn, err := f.main.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(raw any) error {
		raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, arg1, arg2, arg3 string) int {
			if op == sqlite3.SQLITE_TRANSACTION && arg1 == "BEGIN" {
				select {
				case <-entered:
				default:
					close(entered)
				}
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	done := make(chan error, 1)
	go func() {
		_, err := f.authority.Write(ctx, f.main, []Revocation{Source{"s", "writer"}}, 1, noMutation)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("main writer never reached reservation")
	}
	// Same Authority instance: success proves the waiting writer has no mutex.
	appended := make(chan error, 1)
	go func() {
		_, err := f.authority.Append(context.Background(), Source{"s", "independent"}, f.gate.Head, false)
		appended <- err
	}()
	select {
	case err := <-appended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting main writer retained authority mutex")
	}
	cancel()
	if _, err = blocker.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not finish")
	}
	conn, err = f.main.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if active, err := transactionActive(conn); err != nil || active {
		t.Fatal("cancelled BEGIN leaked transaction", active, err)
	}
}
func TestCancelledAuthorityMutexWaitDoesNotReserveMainIndefinitely(t *testing.T) {
	f := newWriterFixture(t)
	entered := make(chan struct{})
	conn, err := f.main.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(raw any) error {
		raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, table, column, database string) int {
			if op == sqlite3.SQLITE_UPDATE && table == "recovery_gate" {
				select {
				case <-entered:
				default:
					close(entered)
				}
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	f.authority.mu.Lock()
	defer f.authority.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.authority.Write(ctx, f.main, nil, 1, noMutation); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("main reservation proof never reached")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled mutex wait did not finish")
	}
	assertMain(t, f, "original", 0, 1)
}
func TestWriteRejectsReadOnlyMainBeforeIntent(t *testing.T) {
	f := newWriterFixture(t)
	readonly, err := storage.OpenExisting(f.mainPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if _, err = f.authority.Write(context.Background(), readonly, []Revocation{Source{"s", "m"}}, 1, noMutation); err == nil {
		t.Fatal("readonly main accepted")
	}
	var count int
	if err = f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&count); err != nil || count != 0 {
		t.Fatal("intent committed before writable proof", count, err)
	}
}
func TestAllInputsValidatedBeforeFirstIntent(t *testing.T) {
	f := newWriterFixture(t)
	_, err := f.authority.Write(context.Background(), f.main, []Revocation{Source{"s", "valid"}, Assertion{ID: "bad", ThroughVersion: -1}}, 1, noMutation)
	if err == nil {
		t.Fatal("invalid input admitted")
	}
	var count int
	if err = f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
func TestTypedEventEncodingDoesNotReplaceStrings(t *testing.T) {
	event, err := encodeRevocation(Source{SceneID: "<>&\u2028\u2029", MessageID: `\u2028`})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"kind\":\"source\",\"sceneId\":\"<>&\u2028\u2029\",\"messageId\":\"\\\\u2028\"}"
	if event.payload != want {
		t.Fatalf("got %q want %q", event.payload, want)
	}
	if event, err = encodeRevocation(Source{SceneID: "s", MessageID: string([]byte{0xed, 0xa0, 0x80})}); err != nil || event.payload != `{"kind":"source","sceneId":"s","messageId":"\ud800"}` {
		t.Fatal("surrogate code units were not preserved", event.payload, err)
	}
	if _, err = encodeRevocation(Source{SceneID: "s", MessageID: string([]byte{0xff})}); err == nil {
		t.Fatal("invalid noncanonical bytes accepted")
	}
	var missing *Source
	if _, err = encodeRevocation(missing); err == nil {
		t.Fatal("nil event accepted")
	}
}

func TestIgnoredMutationErrorStillRefusesACK(t *testing.T) {
	f := newWriterFixture(t)
	_, err := f.authority.Write(context.Background(), f.main, []Revocation{Source{"s", "m"}}, 11, func(tx *MainMutation) error {
		_, _ = tx.Exec("UPDATE data SET value='changed'")
		_, _ = tx.Exec("COMMIT")
		return nil
	})
	if err == nil {
		t.Fatal("ignored SQL denial acknowledged")
	}
	assertMain(t, f, "original", 0, 1)
	assertRestartGap(t, f)
}
func TestSameConnectionMutationInvalidatesAppendCache(t *testing.T) {
	f := newWriterFixture(t)
	head, err := f.authority.Append(context.Background(), Source{"s", "first"}, f.gate.Head, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.authority.conn.ExecContext(context.Background(), "UPDATE main.revocations SET hash='broken' WHERE sequence=1"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.authority.Append(context.Background(), Source{"s", "next"}, head, false); err == nil || !strings.Contains(err.Error(), "chain_invalid") {
		t.Fatal("same-connection corruption escaped", err)
	}
	if f.authority.Scans() != 2 {
		t.Fatal("same-connection mutation not audited")
	}
}
func TestAuthorityAutomaticRollbackDoesNotLeakCleanupErrors(t *testing.T) {
	f := newWriterFixture(t)
	if _, err := f.external.Exec(`CREATE TRIGGER abort_intent BEFORE INSERT ON revocations BEGIN SELECT RAISE(ROLLBACK, 'injected_abort'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.authority.Write(context.Background(), f.main, []Revocation{Source{"s", "m"}}, 11, noMutation)
	if err == nil || !strings.Contains(err.Error(), "injected_abort") || strings.Contains(err.Error(), "no transaction") {
		t.Fatal("native rollback handling", err)
	}
	assertMain(t, f, "original", 0, 1)
	var count int
	if err = f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestExternalCommitAfterUnlockIsNeverStampedVerified(t *testing.T) {
	f := newWriterFixture(t)
	other, err := OpenWriter(context.Background(), f.gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	event, err := encodeRevocation(Source{"s", "own"})
	if err != nil {
		t.Fatal(err)
	}
	var externalHead Head
	f.authority.mu.Lock()
	ownHead, err := f.authority.appendLocked(context.Background(), event, f.gate.Head, false, func(ctx context.Context, conn *sql.Conn, database string) error {
		if err := execCommit(ctx, conn, database); err != nil {
			return err
		}
		own := Head{InstanceID: f.gate.Head.InstanceID, Sequence: 1}
		if err := f.external.QueryRow("SELECT hash FROM revocations WHERE sequence=1").Scan(&own.Hash); err != nil {
			return err
		}
		var err error
		externalHead, err = other.Append(ctx, Source{"s", "external"}, own, false)
		return err
	})
	f.authority.mu.Unlock()
	if err != nil || ownHead.Sequence != 1 || externalHead.Sequence != 2 {
		t.Fatal(ownHead, externalHead, err)
	}
	if _, err = f.authority.Append(context.Background(), Source{"s", "later"}, ownHead, false); err == nil || !strings.Contains(err.Error(), "head_changed") {
		t.Fatal("external commit stamped as verified", err)
	}
	if f.authority.Scans() != 2 {
		t.Fatal("external post-unlock commit not audited")
	}
}
func TestCrashAfterIntentRequiresReconciliationOnRestart(t *testing.T) {
	f := newWriterFixture(t)
	command := exec.Command(os.Args[0], "-test.run=^TestAuthorityCrashProcessHelper$")
	command.Env = append(os.Environ(), "HUMANENGINE_SYNTHETIC_CRASH_MAIN="+f.mainPath, "HUMANENGINE_SYNTHETIC_CRASH_AUTHORITY="+f.gate.Authority)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("crash helper: %v %s", err, output)
	}
	assertMain(t, f, "original", 0, 1)
	assertRestartGap(t, f)
}
func TestAuthorityCrashProcessHelper(t *testing.T) {
	path := os.Getenv("HUMANENGINE_SYNTHETIC_CRASH_MAIN")
	if path == "" {
		return
	}
	authority, err := OpenWriter(context.Background(), os.Getenv("HUMANENGINE_SYNTHETIC_CRASH_AUTHORITY"))
	if err != nil {
		t.Fatal(err)
	}
	main, err := storage.OpenExisting(path, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = authority.write(context.Background(), main, []Revocation{Source{"s", "crashed"}}, 11, noMutation, writeHooks{afterIntent: func(Head) error { os.Exit(73); return nil }})
	t.Fatalf("crash point not reached: %v", err)
}

func TestTypedWriteBytesAndChainMatchTypeScriptOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/write-events-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Sources map[string]string `json:"sources"`
		Rows    []struct {
			Input                           json.RawMessage `json:"input"`
			Payload                         string          `json:"payload"`
			Before, After, Repeated, Forced Head
		}
	}
	if err = json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	for path, want := range oracle.Sources {
		source, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(source)
		if hex.EncodeToString(digest[:]) != want {
			t.Fatalf("TypeScript oracle stale: %s", path)
		}
	}
	f := newWriterFixture(t)
	for _, row := range oracle.Rows {
		var tag struct {
			Kind string `json:"kind"`
		}
		if err = json.Unmarshal(row.Input, &tag); err != nil {
			t.Fatal(err)
		}
		var event Revocation
		switch tag.Kind {
		case "source":
			event = &Source{}
		case "fingerprint":
			event = &Fingerprint{}
		case "assertion":
			event = &Assertion{}
		case "security_state":
			event = &SecurityState{}
		case "source_edit_head":
			event = &SourceEditHead{}
		case "assertion_eligibility":
			event = &AssertionEligibility{}
		case "configuration_flags":
			event = &ConfigurationFlags{}
		case "scene_flags":
			event = &SceneFlags{}
		case "habit_delete":
			event = &HabitDelete{}
		case "meme_delete":
			event = &MemeDelete{}
		case "meme_state":
			event = &MemeState{}
		case "experience_delete":
			event = &ExperienceDelete{}
		case "experience_state":
			event = &ExperienceState{}
		default:
			t.Fatalf("unsupported fixture %s", tag.Kind)
		}
		if err = json.Unmarshal(row.Input, event); err != nil {
			t.Fatal(err)
		}
		encoded, err := encodeRevocation(event)
		if err != nil {
			t.Fatal(err)
		}
		if encoded.payload != row.Payload {
			t.Fatalf("%s payload mismatch\nGo: %s\nTS: %s", tag.Kind, encoded.payload, row.Payload)
		}
		after, err := f.authority.Append(context.Background(), event, row.Before, false)
		if err != nil || after != row.After {
			t.Fatalf("%s append: %+v %v", tag.Kind, after, err)
		}
		repeated, err := f.authority.Append(context.Background(), event, after, false)
		if err != nil || repeated != row.Repeated {
			t.Fatalf("%s repeat: %+v %v", tag.Kind, repeated, err)
		}
		forced, err := f.authority.Append(context.Background(), event, repeated, true)
		if err != nil || forced != row.Forced {
			t.Fatalf("%s force: %+v %v", tag.Kind, forced, err)
		}
	}
	if len(oracle.Rows) != 13 || f.authority.Scans() != 1 {
		t.Fatal("oracle coverage/cache", len(oracle.Rows), f.authority.Scans())
	}
}
func TestPublicationPoolWaitDoesNotHoldAuthority(t *testing.T) {
	f := newWriterFixture(t)
	held, err := f.main.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	baseline := f.main.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		done <- f.authority.Publish(ctx, f.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
	}()
	deadline := time.After(5 * time.Second)
	for f.main.Stats().WaitCount == baseline {
		select {
		case <-deadline:
			t.Fatal("publication did not wait for pool")
		default:
			runtime.Gosched()
		}
	}
	appendCtx, appendCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer appendCancel()
	if _, err = f.authority.Append(appendCtx, Source{"s", "independent"}, f.gate.Head, false); err != nil {
		t.Fatal("publication held authority while waiting for main pool", err)
	}
	held.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "head_changed") {
			t.Fatal("gap missed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not resume")
	}
}

func TestPrepareWriteSerializesPreviousStateAndRevision(t *testing.T) {
	f := newWriterFixture(t)
	if _, err := f.main.Exec("UPDATE data SET value='0'"); err != nil {
		t.Fatal(err)
	}
	otherMain, err := storage.OpenExisting(f.mainPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer otherMain.Close()
	readonly, err := storage.OpenExisting(f.mainPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	firstRead := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	secondRead := make(chan int, 1)
	prepare := func(hold bool) func(*MainRead) (WritePlan, error) {
		return func(read *MainRead) (WritePlan, error) {
			rows, err := read.Query("SELECT value FROM data")
			if err != nil {
				return WritePlan{}, err
			}
			if !rows.Next() {
				return WritePlan{}, errors.New("missing revision")
			}
			var previous int
			if err = rows.Scan(&previous); err != nil {
				return WritePlan{}, err
			}
			if err = rows.Close(); err != nil {
				return WritePlan{}, err
			}
			if hold {
				close(firstRead)
				<-release
			} else {
				secondRead <- previous
			}
			next := previous + 1
			return WritePlan{Intents: []Revocation{Source{"s", fmt.Sprintf("revision%d", next)}}, MinimumReader: 1, Apply: func(tx *MainMutation) error {
				_, err := tx.Exec("UPDATE data SET value=?", next)
				return err
			}}, nil
		}
	}
	go func() {
		_, err := f.authority.PrepareWrite(context.Background(), f.main, prepare(true))
		firstDone <- err
	}()
	<-firstRead
	// Preparation holds main's native writer reservation, but no authority lock.
	// A real independent WAL query must still see the previous acknowledged state.
	readDone := make(chan error, 1)
	go func() {
		value, err := publishValue(f.authority, readonly)
		if err == nil && value != "0" {
			err = fmt.Errorf("read %q", value)
		}
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("preparation held authority or blocked WAL read")
	}
	go func() {
		_, err := f.authority.PrepareWrite(context.Background(), otherMain, prepare(false))
		secondDone <- err
	}()
	close(release)
	for _, done := range []chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("prepared writer failed to finish")
		}
	}
	if previous := <-secondRead; previous != 1 {
		t.Fatal("stale previous revision used", previous)
	}
	assertMain(t, f, "2", 2, 1)
}
func TestPrepareReadCapabilityRejectsWritesAndExpires(t *testing.T) {
	for _, query := range []string{"UPDATE data SET value='changed' RETURNING value", "COMMIT", "PRAGMA query_only=OFF", "CREATE TABLE bypass(value TEXT)"} {
		t.Run(query, func(t *testing.T) {
			f := newWriterFixture(t)
			_, err := f.authority.PrepareWrite(context.Background(), f.main, func(read *MainRead) (WritePlan, error) {
				_, _ = read.Query(query) // Even ignored errors must prevent every intent.
				return WritePlan{Intents: []Revocation{Source{"s", "m"}}, MinimumReader: 11, Apply: noMutation}, nil
			})
			if err == nil {
				t.Fatal("preparation SQL denial ignored")
			}
			assertMain(t, f, "original", 0, 1)
			var count int
			if err = f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&count); err != nil || count != 0 {
				t.Fatal("intent committed after invalid preparation", count, err)
			}
		})
	}
	f := newWriterFixture(t)
	var retained *MainRead
	if _, err := f.authority.PrepareWrite(context.Background(), f.main, func(read *MainRead) (WritePlan, error) {
		retained = read
		return WritePlan{MinimumReader: 1, Apply: noMutation}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := retained.Query("SELECT value FROM data"); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("read capability outlived snapshot", err)
	}
}
func TestPrepareFailureDoesNotCreateIntent(t *testing.T) {
	for _, mode := range []string{"error", "panic", "invalid_plan"} {
		t.Run(mode, func(t *testing.T) {
			f := newWriterFixture(t)
			func() {
				defer func() {
					if mode == "panic" && recover() == nil {
						t.Error("preparation panic disappeared")
					}
				}()
				_, err := f.authority.PrepareWrite(context.Background(), f.main, func(read *MainRead) (WritePlan, error) {
					if mode == "panic" {
						panic("injected")
					}
					if mode == "error" {
						return WritePlan{}, errors.New("injected")
					}
					return WritePlan{Intents: []Revocation{Source{"s", "m"}}, MinimumReader: 0, Apply: noMutation}, nil
				})
				if err == nil {
					t.Fatal("preparation failure accepted")
				}
			}()
			assertMain(t, f, "original", 0, 1)
			var count int
			if err := f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
			if _, err := f.authority.Write(context.Background(), f.main, nil, 1, noMutation); err != nil {
				t.Fatal("preparation failure poisoned next transaction", err)
			}
		})
	}
}

func TestMainAutomaticRollbackKeepsDurableIntentWithoutACK(t *testing.T) {
	f := newWriterFixture(t)
	if _, err := f.main.Exec(`CREATE TRIGGER abort_mutation BEFORE UPDATE ON data BEGIN SELECT RAISE(ROLLBACK, 'injected_main_abort'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.authority.Write(context.Background(), f.main, []Revocation{Source{"s", "m"}}, 11, func(tx *MainMutation) error { _, err := tx.Exec("UPDATE data SET value='changed'"); return err })
	if err == nil || !strings.Contains(err.Error(), "injected_main_abort") || strings.Contains(err.Error(), "no transaction") {
		t.Fatal("native main rollback handling", err)
	}
	assertMain(t, f, "original", 0, 1)
	assertRestartGap(t, f)
}

func TestDiagnosticHeadCannotAuthorizeMainGap(t *testing.T) {
	f := newWriterFixture(t)
	committed, err := f.authority.Append(context.Background(), Source{"s", "m"}, f.gate.Head, false)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := f.authority.Head(context.Background())
	if err != nil || observed != committed {
		t.Fatal(observed, err)
	}
	if f.authority.Scans() != 1 {
		t.Fatal("diagnostic head rescanned controlled history")
	}
	assertRestartGap(t, f)
}
func TestWriterKeepsAuthorityIndependentAndRejectsItsSymlink(t *testing.T) {
	f := newWriterFixture(t)
	link := filepath.Join(filepath.Dir(f.gate.Authority), "authority-link")
	if err := os.Symlink(f.gate.Authority, link); err != nil {
		t.Fatal(err)
	}
	if linked, err := OpenWriter(context.Background(), link); err == nil {
		linked.Close()
		t.Fatal("symlink authority accepted")
	}
	main, err := sql.Open("sqlite3", filepath.Join(filepath.Dir(f.gate.Authority), "parent-main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	if _, err = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	save(t, main, f.gate)
	if _, err = f.authority.Write(context.Background(), main, []Revocation{Source{"s", "m"}}, 1, noMutation); err == nil || !strings.Contains(err.Error(), "not_independent") {
		t.Fatal("authority inside main data directory accepted", err)
	}
	head, err := f.authority.Head(context.Background())
	if err != nil || head != f.gate.Head {
		t.Fatal("invalid path committed intent", head, err)
	}
}

func TestAutomaticRollbackPoisonsMutationCapability(t *testing.T) {
	for _, firstOperation := range []string{"exec", "query"} {
		for _, withIntent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/intent_%v", firstOperation, withIntent), func(t *testing.T) {
				f := newWriterFixture(t)
				if _, err := f.main.Exec(`CREATE TRIGGER abort_first BEFORE UPDATE ON data WHEN NEW.value='abort' BEGIN SELECT RAISE(ROLLBACK,'abort_first'); END`); err != nil {
					t.Fatal(err)
				}
				var intents []Revocation
				if withIntent {
					intents = []Revocation{Source{"s", "m"}}
				}
				var firstErr, secondErr, queryErr error
				_, err := f.authority.Write(context.Background(), f.main, intents, 11, func(tx *MainMutation) error {
					if firstOperation == "exec" {
						_, firstErr = tx.Exec("UPDATE data SET value='abort'")
					} else {
						rows, err := tx.Query("UPDATE data SET value='abort' RETURNING value")
						firstErr = err
						if err == nil {
							for rows.Next() {
							}
							firstErr = rows.Err()
							_ = rows.Close()
						}
					}
					_, secondErr = tx.Exec("UPDATE data SET value='unacknowledged'")
					rows, err := tx.Query("SELECT value FROM data")
					queryErr = err
					if rows != nil {
						_ = rows.Close()
					}
					return nil // Ignoring errors must never escape the transaction.
				})
				if firstErr == nil || err == nil {
					t.Fatalf("first error/write result = %v / %v", firstErr, err)
				}
				assertMain(t, f, "original", 0, 1)
				if secondErr == nil || queryErr == nil {
					t.Fatalf("poisoned capability allowed Exec/Query: %v / %v", secondErr, queryErr)
				}
				if withIntent {
					assertRestartGap(t, f)
				} else {
					value, err := publishValue(f.authority, f.main)
					if err != nil || value != "original" {
						t.Fatalf("failed write escaped publication: %q %v", value, err)
					}
				}
			})
		}
	}
}
func TestPendingRowsCannotExecuteAfterAutomaticRollback(t *testing.T) {
	f := newWriterFixture(t)
	if _, err := f.main.Exec(`CREATE TRIGGER abort_first BEFORE UPDATE ON data WHEN NEW.value='abort' BEGIN SELECT RAISE(ROLLBACK,'abort_first'); END`); err != nil {
		t.Fatal(err)
	}
	var next bool
	var rowsErr error
	_, err := f.authority.Write(context.Background(), f.main, nil, 1, func(tx *MainMutation) error {
		rows, err := tx.Query("UPDATE data SET value='unacknowledged' RETURNING value")
		if err != nil {
			return err
		}
		_, _ = tx.Exec("UPDATE data SET value='abort'")
		next = rows.Next()
		rowsErr = rows.Err()
		_ = rows.Close()
		return nil
	})
	if err == nil {
		t.Fatal("ignored rollback accepted")
	}
	assertMain(t, f, "original", 0, 1)
	if next || rowsErr == nil {
		t.Fatalf("pending statement executed after rollback: next=%v err=%v", next, rowsErr)
	}
	value, err := publishValue(f.authority, f.main)
	if err != nil || value != "original" {
		t.Fatalf("pending write escaped publication: %q %v", value, err)
	}
}

func TestDeferredRowsErrorPoisonsMutationAndPreparation(t *testing.T) {
	const query = `WITH values_to_read(value) AS (VALUES(1),(2)) SELECT CASE value WHEN 1 THEN 1 ELSE abs(-9223372036854775808) END FROM values_to_read`
	for _, preparation := range []bool{false, true} {
		t.Run(fmt.Sprintf("prepare_%v", preparation), func(t *testing.T) {
			f := newWriterFixture(t)
			var delayedErr, followingErr error
			consume := func(rows *MainRows, err error) error {
				if err != nil {
					return err
				}
				defer rows.Close()
				if !rows.Next() {
					return errors.New("first query row missing")
				}
				var value int
				if err = rows.Scan(&value); err != nil {
					return err
				}
				if value != 1 {
					return errors.New("first query row changed")
				}
				if rows.Next() {
					return errors.New("failing row unexpectedly succeeded")
				}
				delayedErr = rows.Err()
				return nil
			}
			var err error
			if preparation {
				_, err = f.authority.PrepareWrite(context.Background(), f.main, func(read *MainRead) (WritePlan, error) {
					if err := consume(read.Query(query)); err != nil {
						return WritePlan{}, err
					}
					_, followingErr = read.Query("SELECT value FROM data")
					return WritePlan{MinimumReader: 1, Apply: func(tx *MainMutation) error { _, err := tx.Exec("UPDATE data SET value='unacknowledged'"); return err }}, nil
				})
			} else {
				_, err = f.authority.Write(context.Background(), f.main, nil, 1, func(tx *MainMutation) error {
					if err := consume(tx.Query(query)); err != nil {
						return err
					}
					_, followingErr = tx.Exec("UPDATE data SET value='unacknowledged'")
					return nil
				})
			}
			if delayedErr == nil || followingErr == nil || err == nil {
				t.Fatalf("deferred error was not latched: %v / %v / %v", delayedErr, followingErr, err)
			}
			assertMain(t, f, "original", 0, 1)
			value, err := publishValue(f.authority, f.main)
			if err != nil || value != "original" {
				t.Fatalf("failed write escaped: %q %v", value, err)
			}
		})
	}
}
func TestNativeTransactionLossRejectsCapabilityBeforeStatement(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("read_%v", readOnly), func(t *testing.T) {
			f := newWriterFixture(t)
			conn, err := f.main.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err = beginOwned(context.Background(), conn, "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			capability := &MainMutation{conn: conn, ctx: context.Background(), active: true}
			// Simulate native transaction loss not yet reported to the capability.
			// Its Go active flag deliberately remains true.
			if _, err = conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			if readOnly {
				_, err = (&MainRead{state: capability}).Query("SELECT value FROM data")
			} else {
				_, err = capability.Exec("UPDATE data SET value='unacknowledged'")
			}
			if !errors.Is(err, sql.ErrTxDone) || !strings.Contains(err.Error(), "recovery_main_transaction_lost") {
				t.Fatal("native loss not rejected", err)
			}
			if err = capability.finish(nil); err == nil {
				t.Fatal("lost transaction accepted at finish")
			}
			conn.Close()
			assertMain(t, f, "original", 0, 1)
		})
	}
}
