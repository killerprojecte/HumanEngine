package recoverydomains

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func parse(t *testing.T, s string) any     { t.Helper(); v, e := jsjson.Parse(s); must(t, e); return v }
func stringify(t *testing.T, v any) string { t.Helper(); s, e := encode(v); must(t, e); return s }
func oracle(t *testing.T, name string) *object {
	t.Helper()
	b, e := os.ReadFile("testdata/" + name + "-node.json")
	must(t, e)
	return obj(parse(t, string(b)))
}
func fixtureOptions(t *testing.T, fixture *object) Options {
	pairs := map[string]int{}
	for _, v := range arr(get(fixture, "comparisons")) {
		a := arr(v)
		pairs[str(a[0])+"\x00"+str(a[1])] = int(num(a[2]))
	}
	return Options{Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }, Compare: func(a, b string) (int, error) {
		if a == b {
			return 0, nil
		}
		v, ok := pairs[a+"\x00"+b]
		if !ok {
			return 0, fmt.Errorf("missing actual Node comparison %q %q", a, b)
		}
		return v, nil
	}}
}

type fixture struct {
	owner     *recovery.Maintenance
	conn      *sql.Conn
	db        *sql.DB
	authority *recovery.Authority
	media     string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	must(t, os.Mkdir(authorityPath, 0700))
	external, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	must(t, e)
	_, e = external.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	must(t, e)
	must(t, external.Close())
	must(t, os.Mkdir(filepath.Join(root, "live"), 0700))
	path := filepath.Join(root, "live", "main.sqlite")
	db, e := sql.Open("sqlite3", path)
	must(t, e)
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA foreign_keys=ON;PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE recall_tombstones(scene_id TEXT,message_id TEXT,at TEXT,PRIMARY KEY(scene_id,message_id));CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL)`)
	must(t, e)
	gate := makeObject("head", makeObject("instanceId", "12345678-1234-4123-8123-123456789012", "sequence", float64(0), "hash", strings.Repeat("0", 64)), "authority", authorityPath, "minimumReader", float64(1), "state", "quarantine")
	_, e = db.Exec("INSERT INTO recovery_gate VALUES(1,?)", stringify(t, gate))
	must(t, e)
	a, e := recovery.OpenWriter(ctx, authorityPath)
	must(t, e)
	conn, e := db.Conn(ctx)
	must(t, e)
	owner, e := recovery.AdmitMaintenance(ctx, conn, path, a)
	must(t, e)
	media := filepath.Join(root, "media")
	must(t, os.Mkdir(media, 0700))
	f := &fixture{owner, conn, db, a, media}
	t.Cleanup(func() { conn.Close(); db.Close(); a.Close() })
	return f
}
func seed(t *testing.T, f *fixture, rows *object) {
	t.Helper()
	ctx := context.Background()
	must(t, f.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		if e := exec(ctx, tx, schemaDDL); e != nil {
			return e
		}
		for _, table := range rows.Keys() {
			if table == "memory_experience_trials" {
				if e := exec(ctx, tx, "CREATE TABLE IF NOT EXISTS memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT)"); e != nil {
					return e
				}
			}
			for _, v := range arr(get(rows, table)) {
				r := obj(v)
				columns := r.Keys()
				args := []any{}
				marks := []string{}
				for _, k := range columns {
					args = append(args, get(r, k))
					marks = append(marks, "?")
				}
				if e := exec(ctx, tx, "INSERT INTO "+table+"("+strings.Join(columns, ",")+") VALUES("+strings.Join(marks, ",")+")", args...); e != nil {
					return e
				}
			}
		}
		return nil
	}))
}
func compareRows(t *testing.T, f *fixture, want *object) {
	t.Helper()
	for _, table := range want.Keys() {
		rows, e := f.conn.QueryContext(context.Background(), "SELECT * FROM "+table+" ORDER BY rowid")
		must(t, e)
		columns, e := rows.Columns()
		must(t, e)
		actual := []any{}
		for rows.Next() {
			dest := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range dest {
				ptrs[i] = &dest[i]
			}
			must(t, rows.Scan(ptrs...))
			r := new(object)
			for i, k := range columns {
				v := dest[i]
				if n, ok := v.(int64); ok {
					v = float64(n)
				}
				set(r, k, v)
			}
			actual = append(actual, r)
		}
		must(t, rows.Err())
		must(t, rows.Close())
		normalize := func(items []any) []any {
			out := []any{}
			for _, v := range items {
				r := obj(v).Clone()
				if raw, ok := get(r, "value").(string); ok {
					set(r, "value", parse(t, raw))
				}
				out = append(out, r)
			}
			return out
		}
		a, b := normalize(actual), normalize(arr(get(want, table)))
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s rows differ\n got %s\nwant %s", table, stringify(t, a), stringify(t, b))
		}
	}
}
func TestActualSourceDomainSequence(t *testing.T) {
	node := oracle(t, "domains")
	f := setup(t)
	seed(t, f, obj(get(node, "initial")))
	media := obj(get(node, "media"))
	bytes, e := base64.StdEncoding.DecodeString(str(get(media, "bytes")))
	must(t, e)
	path := filepath.Join(f.media, str(get(media, "id"))+".png")
	must(t, os.WriteFile(path, bytes, 0600))
	o, e := NewWithOptions(f.owner, f.media, fixtureOptions(t, node))
	must(t, e)
	compareRows(t, f, obj(get(node, "afterConstructor")))
	events := []string{}
	for i, v := range arr(get(node, "steps")) {
		step := obj(v)
		raw := stringify(t, get(step, "event"))
		must(t, o.Apply(context.Background(), raw))
		events = append(events, raw)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		must(t, f.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return o.Verify(ctx, tx, events) }))
		cancel()
		t.Run(fmt.Sprint(i), func(t *testing.T) { compareRows(t, f, obj(get(step, "rows"))) })
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatalf("media remains: %v", e)
	}
	if len(o.applied) != len(events) {
		t.Fatal("duplicate events silently deduplicated")
	}
}
func TestActualSourceTrialEvidenceAndRestart(t *testing.T) {
	node := oracle(t, "trials")
	f := setup(t)
	seed(t, f, obj(get(node, "initial")))
	opts := fixtureOptions(t, node)
	port, e := NewTrialsWithOptions(f.owner, opts)
	must(t, e)
	ctx := context.Background()
	verify := func(events []string) error {
		return f.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return port.Verify(ctx, tx, events) })
	}
	must(t, verify(nil))
	for _, v := range arr(get(node, "corruptions")) {
		c := obj(v)
		t.Run(str(get(c, "name")), func(t *testing.T) {
			_, e := f.conn.ExecContext(ctx, "SAVEPOINT corrupt")
			must(t, e)
			_, e = f.conn.ExecContext(ctx, str(get(c, "sql")))
			must(t, e)
			e = verify(nil)
			if e == nil || e.Error() != str(get(c, "error")) {
				t.Fatalf("got %v want %v", e, get(c, "error"))
			}
			_, e = f.conn.ExecContext(ctx, "ROLLBACK TO corrupt;RELEASE corrupt")
			must(t, e)
		})
	}
	event := stringify(t, get(node, "event"))
	must(t, port.Prepare(ctx, []string{event}))
	must(t, port.Apply(ctx, event))
	must(t, verify([]string{event}))
	compareRows(t, f, obj(get(node, "afterSuspend")))
	port, e = NewTrialsWithOptions(f.owner, opts)
	must(t, e)
	must(t, port.Prepare(ctx, []string{event}))
	must(t, port.Apply(ctx, event))
	must(t, verify([]string{event}))
	compareRows(t, f, obj(get(node, "afterRestart")))
	erase := stringify(t, get(node, "erase"))
	port, e = NewTrialsWithOptions(f.owner, opts)
	must(t, e)
	must(t, port.Prepare(ctx, []string{event, erase}))
	must(t, port.Apply(ctx, erase))
	must(t, verify([]string{event, erase}))
	compareRows(t, f, obj(get(node, "afterErase")))
}
func TestDomainTransactionLossAndWrongScope(t *testing.T) {
	node := oracle(t, "domains")
	f := setup(t)
	seed(t, f, obj(get(node, "initial")))
	o, e := NewWithOptions(f.owner, f.media, fixtureOptions(t, node))
	must(t, e)
	_, e = f.conn.ExecContext(context.Background(), `CREATE TRIGGER fail_habit BEFORE DELETE ON habits BEGIN SELECT RAISE(ROLLBACK,'synthetic loss');END`)
	must(t, e)
	event := stringify(t, get(obj(arr(get(node, "steps"))[3]), "event"))
	if e = o.Apply(context.Background(), event); e == nil {
		t.Fatal("transaction loss accepted")
	}
	var count int
	must(t, f.conn.QueryRowContext(context.Background(), "SELECT count(*) FROM habits").Scan(&count))
	if count != 1 || len(o.applied) != 0 {
		t.Fatal("rolled-back state escaped")
	}
	other := setup(t)
	must(t, other.owner.Apply(context.Background(), 1, func(tx *recovery.MaintenanceTx) error {
		if e := o.Verify(context.Background(), tx, nil); e == nil {
			t.Fatal("wrong owner accepted")
		}
		return nil
	}))
	var escaped *recovery.MaintenanceTx
	must(t, f.owner.Apply(context.Background(), 1, func(tx *recovery.MaintenanceTx) error { escaped = tx; return nil }))
	if e = o.Verify(context.Background(), escaped, nil); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("expired scope", e)
	}
}
func TestMemeFilesystemFailurePrecedesDelete(t *testing.T) {
	node := oracle(t, "domains")
	f := setup(t)
	seed(t, f, obj(get(node, "initial")))
	o, e := NewWithOptions(f.owner, f.media, fixtureOptions(t, node))
	must(t, e)
	asset := obj(parse(t, str(get(obj(arr(get(obj(get(node, "initial")), "meme_assets"))[0]), "value"))))
	h, e := o.memeQualification(asset)
	must(t, e)
	id := str(get(asset, "id"))
	event := stringify(t, makeObject("schemaVersion", float64(1), "kind", "meme_delete", "id", id, "contentHash", id, "qualificationHash", h, "format", "png"))
	path := filepath.Join(f.media, id+".png")
	must(t, os.WriteFile(path, []byte("wrong bytes"), 0600))
	if e = o.Apply(context.Background(), event); e == nil || e.Error() != "recovery_meme_content_mismatch" {
		t.Fatal(e)
	}
	var n int
	must(t, f.conn.QueryRowContext(context.Background(), "SELECT count(*) FROM meme_assets WHERE id=?", id).Scan(&n))
	if n != 1 {
		t.Fatal("filesystem error deleted row")
	}
	must(t, os.Remove(path))
	must(t, os.Mkdir(path, 0700))
	if e = o.Apply(context.Background(), event); e == nil {
		t.Fatal("directory accepted as image")
	}
}

func TestActualDomainSourceUnregisteredReference(t *testing.T) {
	node := oracle(t, "domain-source")
	f := setup(t)
	ctx := context.Background()
	must(t, f.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		if e := exec(ctx, tx, "INSERT INTO scenes VALUES(?,?)", "scene", stringify(t, makeObject("adapterId", "adapter", "accountId", "account"))); e != nil {
			return e
		}
		for _, v := range arr(get(node, "cases")) {
			c := obj(v)
			eligible, e := sourceCurrent(ctx, tx, "scene", obj(get(c, "source")), false, true)
			if get(c, "error") != nil {
				if e == nil {
					t.Fatalf("%s invalid ref was silently rejected without source error", get(c, "name"))
				}
			} else {
				if e != nil {
					return e
				}
				if eligible != yes(get(c, "eligible")) {
					t.Fatalf("%s got %v want %v", get(c, "name"), eligible, get(c, "eligible"))
				}
			}
		}
		return nil
	}))
}
func TestUnknownDomainAndTrialStorageAreRejected(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, e := f.conn.ExecContext(ctx, "CREATE TABLE meme_future(id TEXT)")
	must(t, e)
	if _, e = NewWithOptions(f.owner, f.media, Options{}); e == nil || e.Error() != "recovery_unknown_domain_schema" {
		t.Fatal(e)
	}
	_, e = f.conn.ExecContext(ctx, "DROP TABLE meme_future;CREATE TABLE memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT,version INTEGER,value TEXT,future TEXT)")
	must(t, e)
	port, e := NewTrials(f.owner)
	must(t, e)
	if e = port.AssertSupported(ctx); e == nil || e.Error() != "recovery_unknown_experience_trial_schema" {
		t.Fatal(e)
	}
	_, e = f.conn.ExecContext(ctx, "DROP TABLE memory_experience_trials;CREATE TABLE memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT,version INTEGER,value TEXT);INSERT INTO memory_experience_trials VALUES('p','s',1,x'7b7d')")
	must(t, e)
	if e = port.AssertSupported(ctx); e == nil || e.Error() != "recovery_experience_trial_material_invalid" {
		t.Fatal(e)
	}
}

func TestMemeUnlinkThenSQLRollbackCanRestart(t *testing.T) {
	node := oracle(t, "domains")
	f := setup(t)
	seed(t, f, obj(get(node, "initial")))
	options := fixtureOptions(t, node)
	o, e := NewWithOptions(f.owner, f.media, options)
	must(t, e)
	asset := obj(parse(t, str(get(obj(arr(get(obj(get(node, "initial")), "meme_assets"))[0]), "value"))))
	h, e := o.memeQualification(asset)
	must(t, e)
	id := str(get(asset, "id"))
	event := stringify(t, makeObject("schemaVersion", float64(1), "kind", "meme_delete", "id", id, "contentHash", id, "qualificationHash", h, "format", "png"))
	bytes, e := base64.StdEncoding.DecodeString(str(get(obj(get(node, "media")), "bytes")))
	must(t, e)
	path := filepath.Join(f.media, id+".png")
	must(t, os.WriteFile(path, bytes, 0600))
	ctx := context.Background()
	_, e = f.conn.ExecContext(ctx, `CREATE TRIGGER fail_media BEFORE DELETE ON meme_assets BEGIN SELECT RAISE(ABORT,'synthetic SQL failure after unlink');END`)
	must(t, e)
	if e = o.Apply(ctx, event); e == nil {
		t.Fatal("SQL failure accepted")
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("file unlink incorrectly rolled back", e)
	}
	var count int
	must(t, f.conn.QueryRowContext(ctx, "SELECT count(*) FROM meme_assets WHERE id=?", id).Scan(&count))
	if count != 1 {
		t.Fatal("failed SQL committed row deletion")
	}
	if len(o.applied) != 0 {
		t.Fatal("failed event entered final expectations")
	}
	_, e = f.conn.ExecContext(ctx, "DROP TRIGGER fail_media")
	must(t, e)
	restart, e := NewWithOptions(f.owner, f.media, options)
	must(t, e)
	must(t, restart.Apply(ctx, event))
	must(t, f.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return restart.Verify(ctx, tx, []string{event}) }))
	must(t, f.conn.QueryRowContext(ctx, "SELECT count(*) FROM meme_assets WHERE id=?", id).Scan(&count))
	if count != 0 {
		t.Fatal("restart left deleted row")
	}
}

func TestConstructorFailureRetainsCommittedSchemas(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, e := f.conn.ExecContext(ctx, `CREATE TABLE meme_assets(id TEXT PRIMARY KEY,value TEXT NOT NULL);INSERT INTO meme_assets VALUES('bad','{"id":"bad","tags":null,"analysisState":"running"}')`)
	must(t, e)
	if _, e = NewWithOptions(f.owner, f.media, Options{}); e == nil {
		t.Fatal("invalid media tags accepted")
	}
	var count int
	must(t, f.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name IN ('habits','habit_deleted','meme_sources','meme_search_concepts')").Scan(&count))
	if count != 4 {
		t.Fatal("constructor rolled back earlier committed schemas", count)
	}
	must(t, f.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='memory_experience_outcomes'").Scan(&count))
	if count != 0 {
		t.Fatal("constructor advanced beyond failed media initialization")
	}
	var raw string
	must(t, f.conn.QueryRowContext(ctx, "SELECT value FROM meme_assets WHERE id='bad'").Scan(&raw))
	if str(get(obj(parse(t, raw)), "analysisState")) != "running" {
		t.Fatal("media rebuild partially committed")
	}
}

func TestActualTrialReceiptSchema(t *testing.T) {
	node := oracle(t, "trials")
	for i, v := range arr(get(node, "receiptValidation")) {
		c := obj(v)
		e := validateReceipt(obj(get(c, "value")))
		if (e == nil) != yes(get(c, "valid")) {
			t.Fatalf("receipt %d got %v want valid=%v", i, e, get(c, "valid"))
		}
	}
}

func TestMemeDeleteRejectsOuterTransactionBeforeUnlink(t *testing.T) {
	node := oracle(t, "domains")
	f := setup(t)
	ctx := context.Background()
	seed(t, f, obj(get(node, "initial")))
	o, e := NewWithOptions(f.owner, f.media, fixtureOptions(t, node))
	must(t, e)
	asset := obj(parse(t, str(get(obj(arr(get(obj(get(node, "initial")), "meme_assets"))[0]), "value"))))
	h, e := o.memeQualification(asset)
	must(t, e)
	id := str(get(asset, "id"))
	bytes, e := base64.StdEncoding.DecodeString(str(get(obj(get(node, "media")), "bytes")))
	must(t, e)
	path := filepath.Join(f.media, id+".png")
	must(t, os.WriteFile(path, bytes, 0600))
	event := stringify(t, makeObject("schemaVersion", float64(1), "kind", "meme_delete", "id", id, "contentHash", id, "qualificationHash", h, "format", "png"))
	_, e = f.conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	must(t, e)
	if e = o.Apply(ctx, event); e == nil || e.Error() != "recovery_reconcile_transaction_active" {
		t.Fatal(e)
	}
	actual, e := os.ReadFile(path)
	must(t, e)
	if string(actual) != string(bytes) {
		t.Fatal("outer transaction unlinked file")
	}
	_, e = f.conn.ExecContext(ctx, "ROLLBACK")
	must(t, e)
	must(t, o.Apply(ctx, event))
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("autocommit delete did not unlink", e)
	}
}
