package memoryservice

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	_ "github.com/mattn/go-sqlite3"
	"math"
)

type scopedFixture struct {
	main     *sql.DB
	recovery *recovery.Authority
	store    *assertions.Store
	events   *sourceevents.Store
}

func scopedSetup(t *testing.T) scopedFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	if e := os.Mkdir(authorityPath, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(root, "live"), 0700); e != nil {
		t.Fatal(e)
	}
	main, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite")+"?_foreign_keys=on")
	if e != nil {
		t.Fatal(e)
	}
	main.SetMaxOpenConns(1)
	_, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id));INSERT INTO scenes VALUES('s','{"id":"s","adapterId":"adapter","accountId":"account"}')`)
	if e != nil {
		t.Fatal(e)
	}
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: 1, State: "ready"}
	raw, _ := json.Marshal(gate)
	if _, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	ra, e := recovery.OpenWriter(ctx, authorityPath)
	if e != nil {
		t.Fatal(e)
	}
	store, e := assertions.New(main, ra, assertions.Options{})
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	events, e := sourceevents.New(main, ra, sourceevents.Options{ID: func() string { return "source-event" }, Now: func() string { return "2026-10-28T12:00:00.000Z" }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ra.Close(); main.Close() })
	return scopedFixture{main, ra, store, events}
}
func parseObject(t *testing.T, raw string) *assertions.Object {
	t.Helper()
	v, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v.(*assertions.Object)
}
func testOptions(t *testing.T) assertionauthority.Options {
	t.Helper()
	p, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	return assertionauthority.Options{DateParser: p, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }, NowMillis: func() int64 { return 1793188800000 }}
}

// recordingNotifications records the explicit incomplete runtime boundary; it
// is test-only, not a production substitute for semantic authority.
type recordingNotifications struct{ refs []Reference }

func (n *recordingNotifications) AssertionCommitted(_ string, r Reference) error {
	n.refs = append(n.refs, r)
	return nil
}
func serviceSetup(t *testing.T) (*Service, scopedFixture) {
	t.Helper()
	f := scopedSetup(t)
	ctx := context.Background()
	options := testOptions(t)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	plans, e := commitments.New(f.main, f.recovery, commitments.Options{Compare: options.Compare, DateParser: options.DateParser})
	must(e)
	must(plans.Initialize(ctx))
	relations, e := relationstorage.New(f.main, f.recovery)
	must(e)
	must(relations.Initialize(ctx))
	k, e := knowledge.New(f.main, f.recovery)
	must(e)
	sem, e := semantic.New(f.main, f.recovery)
	must(e)
	p, e := projections.New(ctx, f.main, f.recovery, func() bool { return true }, projections.AssertionReader(f.store, options), projections.Options{})
	must(e)
	flag := func(commitments.Context) (bool, error) { return true, nil }
	ws, e := workingset.New(ctx, f.main, f.recovery, workingset.Ports{Enabled: func() bool { return true }, ResolveCurrent: ResolveWorkingSet(plans, f.store, flag, options)})
	must(e)
	s, e := New(Dependencies{Assertions: f.store, Commitments: plans, Relations: relations, Projections: p, WorkingSet: ws, Knowledge: k, Semantic: sem, Notifications: &recordingNotifications{}, Dirty: func() {}, Log: func(level, category, message string) { t.Log(level, category, message) }, CommitmentsEnabled: flag, Authority: options})
	must(e)
	return s, f
}
func putRecord(t *testing.T, f scopedFixture, id, extra string) *Object {
	t.Helper()
	r := parseObject(t, fmt.Sprintf(`{"id":%q,"scope":{"kind":"scene","sceneId":"s","personId":"p"},"kind":"experience","content":"Synthetic","sources":[],"status":"active","learnedAt":"2000-01-01T00:00:00.000Z"%s}`, id, extra))
	raw, e := jsjson.Stringify(r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.main.Exec("INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,'scene:s','p','Synthetic',?,?)", id, raw, id); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestCaptureActualStoresOneConnection(t *testing.T) {
	s, f := serviceSetup(t)
	ctx := context.Background()
	c := Context{SceneID: "s"}
	record := putRecord(t, f, "a", "")
	returned := record.Clone()
	_ = returned.Set("score", 42.0)
	got, e := s.CaptureParentReferences(ctx, c, []*Object{returned, returned})
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 || got[0] != (Reference{"a", 1, 0}) || got[1] != got[0] {
		t.Fatalf("capture %v", got)
	}
	// JSON property order is not Node deep strict object equality.
	reordered := new(Object)
	members := record.Members()
	for i := len(members) - 1; i >= 0; i-- {
		_ = reordered.Set(members[i].Key, members[i].Value)
	}
	got, e = s.CaptureParentReferences(ctx, c, []*Object{reordered})
	if e != nil || len(got) != 1 {
		t.Fatalf("order %v %v", got, e)
	}
	stale := record.Clone()
	_ = stale.Set("content", "not current")
	got, e = s.CaptureExposedReferences(ctx, c, []*Object{stale})
	if e != nil || len(got) != 0 {
		t.Fatalf("stale %v %v", got, e)
	}
	// Only the source parent API is bounded; exposed capture preserves duplicates.
	many := make([]*Object, 41)
	for i := range many {
		many[i] = record
	}
	if _, e = s.CaptureParentReferences(ctx, c, many); e == nil || e.Error() != "workspace_candidate_limit" {
		t.Fatalf("parent limit %v", e)
	}
	got, e = s.CaptureExposedReferences(ctx, c, many)
	if e != nil || len(got) != 41 {
		t.Fatalf("exposure %d %v", len(got), e)
	}
	if e = f.store.Read(ctx, func(r *assertions.Reader) error {
		state, e := r.State("a")
		if state.Version != 1 {
			t.Fatalf("duplicate import %+v", state)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
func TestRefreshActuallyInvalidatesAndNoPrematureCapture(t *testing.T) {
	s, f := serviceSetup(t)
	r := putRecord(t, f, "unsupported", `,"lineageStatus":"verified"`)
	got, e := s.CaptureExposedReferences(context.Background(), Context{SceneID: "s"}, []*Object{r})
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 0 {
		t.Fatal(got)
	}
	if e = f.store.Read(context.Background(), func(read *assertions.Reader) error {
		current, e := read.Record("unsupported")
		if e != nil {
			return e
		}
		if text(current, "status") != "invalidated" {
			t.Fatal(current)
		}
		state, e := read.State("unsupported")
		if state.Version != 0 || state.DeletionEpoch == 0 {
			t.Fatalf("state %+v", state)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
func TestSimulationCaptureBeforeDependenciesAndLimit(t *testing.T) {
	s := new(Service)
	got, e := s.CaptureParentReferences(context.Background(), Context{Simulation: true}, make([]*Object, 100))
	if e != nil || len(got) != 0 {
		t.Fatal(got, e)
	}
	if _, e = New(Dependencies{}); e == nil {
		t.Fatal("missing owners accepted")
	}
}
func TestBodyEqualityDomain(t *testing.T) {
	if deepEqual(0.0, math.Copysign(0, -1)) || !deepEqual(math.NaN(), math.NaN()) {
		t.Fatal("Object.is numbers")
	}
	a := parseObject(t, `{"x":null}`)
	b := parseObject(t, `{}`)
	if deepEqual(a, b) {
		t.Fatal("presence")
	}
	if deepEqual([]any{1.0, 2.0}, []any{2.0, 1.0}) {
		t.Fatal("array order")
	}
}

func TestActualSourceCaptureOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatalf("source oracle must be generated in the authorized validation lane: %v", e)
	}
	var fixtures struct {
		Hashes   map[string]string `json:"hashes"`
		Outcomes []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
			Error string          `json:"error"`
		} `json:"outcomes"`
	}
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	required := []string{"src/memory.ts", "src/memory/write/revision-coordinator.ts", "src/memory/lifecycle/proof-invalidation.ts", "src/memory/semantic/notifications.ts", "backend/tests/generate-memory-service-fixtures.mjs", "package-lock.json"}
	if len(fixtures.Hashes) != len(required) {
		t.Fatal("source manifest entry count")
	}
	for _, path := range required {
		data, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil {
			t.Fatal(err)
		}
		if fixtures.Hashes[path] != fmt.Sprintf("%x", sha256.Sum256(data)) {
			t.Fatalf("source manifest mismatch: %s", path)
		}
	}
	s, f := serviceSetup(t)
	ctx := context.Background()
	c := Context{SceneID: "s"}
	r := putRecord(t, f, "a", "")
	for _, item := range fixtures.Outcomes {
		var got any
		var err error
		switch item.Name {
		case "legacy_duplicate_score":
			scored := r.Clone()
			_ = scored.Set("score", 42.0)
			got, err = s.CaptureParentReferences(ctx, c, []*Object{scored, scored})
		case "reordered":
			rev := new(Object)
			m := r.Members()
			for i := len(m) - 1; i >= 0; i-- {
				_ = rev.Set(m[i].Key, m[i].Value)
			}
			got, err = s.CaptureParentReferences(ctx, c, []*Object{rev})
		case "stale":
			stale := r.Clone()
			_ = stale.Set("content", "not current")
			got, err = s.CaptureExposedReferences(ctx, c, []*Object{stale})
		case "parent_41", "exposed_41":
			many := make([]*Object, 41)
			for i := range many {
				many[i] = r
			}
			if item.Name == "parent_41" {
				got, err = s.CaptureParentReferences(ctx, c, many)
			} else {
				got, err = s.CaptureExposedReferences(ctx, c, many)
			}
		case "simulation_100":
			got, err = s.CaptureParentReferences(ctx, Context{Simulation: true}, make([]*Object, 100))
		case "refresh_capture":
			unsupported := putRecord(t, f, "unsupported", `,"lineageStatus":"verified"`)
			got, err = s.CaptureExposedReferences(ctx, c, []*Object{unsupported})
		case "unsupported_row":
			err = f.store.Read(ctx, func(read *assertions.Reader) error { v, e := read.Record("unsupported"); got = v; return e })
		case "states":
			continue // Native tests assert state; SQL column-shape fixture retained for audit.
		default:
			t.Fatalf("unknown oracle operation %s", item.Name)
		}
		if item.Error != "" {
			if err == nil || err.Error() != item.Error {
				t.Fatalf("%s: %v want %s", item.Name, err, item.Error)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", item.Name, err)
		}
		var encoded []byte
		if o, ok := got.(*Object); ok {
			encoded, err = jsjson.Marshal(o)
		} else {
			encoded, err = json.Marshal(got)
		}
		if err != nil {
			t.Fatal(err)
		}
		actual, err := jsjson.Parse(string(encoded))
		if err != nil {
			t.Fatal(err)
		}
		expected, err := jsjson.Parse(string(item.Value))
		if err != nil {
			t.Fatal(err)
		}
		if !deepEqual(actual, expected) {
			t.Fatalf("%s got %s want %s", item.Name, encoded, item.Value)
		}
	}
}

func TestSemanticCurrentActualScopedProvenance(t *testing.T) {
	s, f := serviceSetup(t)
	ctx := context.Background()
	record := putRecord(t, f, "semantic", "")
	source := parseObject(t, `{"sceneId":"s","messageId":"message","senderId":"p"}`)
	_ = record.Set("sources", []any{source})
	raw, e := jsjson.Stringify(record)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.main.Exec("UPDATE memories SET value=? WHERE id='semantic'", raw); e != nil {
		t.Fatal(e)
	}
	if _, e = f.main.Exec(`INSERT INTO memory_supports(memory_id,value) VALUES('semantic','[{"sources":[{"sceneId":"s","messageId":"message","senderId":"p"}],"parents":[],"parentReferences":[]}]')`); e != nil {
		t.Fatal(e)
	}
	refs, e := s.CaptureParentReferences(ctx, Context{SceneID: "s"}, []*Object{record})
	if e != nil || len(refs) != 1 {
		t.Fatalf("capture %v %v", refs, e)
	}
	current, e := s.SemanticCurrent(ctx, Context{SceneID: "s"}, refs[0])
	if e != nil || current == nil {
		t.Fatalf("semantic current %v %v", current, e)
	}
	if text(current, "rootCoverage") != "complete" || text(current, "dependencyKey") == "" {
		t.Fatal(current)
	}
	roots, _ := value(current, "roots").([]any)
	expected, e := semanticHash([]any{"s", "message"})
	if e != nil || len(roots) != 1 || roots[0] != expected {
		t.Fatalf("roots %v %v", roots, e)
	}
	if _, e = f.main.Exec("INSERT INTO recall_tombstones VALUES('s','message','2026-01-01')"); e != nil {
		t.Fatal(e)
	}
	current, e = s.SemanticCurrent(ctx, Context{SceneID: "s"}, refs[0])
	if e != nil || current != nil {
		t.Fatalf("recalled provenance %v %v", current, e)
	}
}

func TestPostCommitProjectionFaultLogsWithoutFailingCapture(t *testing.T) {
	base, f := serviceSetup(t)
	warnings := [][3]string{}
	d := base.d
	d.Log = func(level, category, message string) {
		warnings = append(warnings, [3]string{level, category, message})
	}
	s, e := New(d)
	if e != nil {
		t.Fatal(e)
	}
	r := putRecord(t, f, "committed-before-flush", "")
	// Native SQL fault, not an overridden Flush. Legacy capture commits an actual
	// assertion revision before the real projection processor attempts its claim.
	if _, e = f.main.Exec("DROP TABLE memory_projection_outbox"); e != nil {
		t.Fatal(e)
	}
	got, e := s.CaptureParentReferences(context.Background(), Context{SceneID: "s"}, []*Object{r})
	if e != nil || len(got) != 1 || got[0].Version != 1 {
		t.Fatalf("successful capture hidden by postcommit fault: %v %v", got, e)
	}
	if len(warnings) != 1 || warnings[0] != [3]string{"warn", "memory", "memory_projection_refresh_failed"} {
		t.Fatalf("warnings %v", warnings)
	}
	if e = f.store.Read(context.Background(), func(read *assertions.Reader) error {
		state, e := read.State("committed-before-flush")
		if e != nil {
			return e
		}
		if state.Version != 1 || state.Deleted {
			t.Fatalf("committed state lost: %+v", state)
		}
		revision, e := read.Revision("committed-before-flush", 1, "scene:s")
		if e == nil && revision == nil {
			t.Fatal("committed revision absent")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	d.Log = nil
	if _, e = New(d); e == nil {
		t.Fatal("missing runtime warning owner accepted")
	}
}
