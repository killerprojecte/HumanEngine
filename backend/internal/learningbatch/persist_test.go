package learningbatch

import (
	"context"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"testing"
	"time"
)

type fixtureReads struct {
	f     scopedFixture
	after func(*Object) error
}

func (f fixtureReads) Settings(context.Context) (Settings, error) {
	return Settings{Timezone: "UTC"}, nil
}
func (f fixtureReads) Enabled(context.Context, string) (bool, error)          { return true, nil }
func (f fixtureReads) Recalled(context.Context, string, string) (bool, error) { return false, nil }
func (f fixtureReads) Search(context.Context, string, string) ([]*Object, error) {
	return nil, errors.New("unexpected_search")
}
func (f fixtureReads) Targets(context.Context, string, []*Object, []*Object, []any) (TargetScope, error) {
	return TargetScope{}, errors.New("unexpected_targets")
}
func (f fixtureReads) UnsupportedPronouns(context.Context, string, *Object, []any) (bool, error) {
	return false, nil
}
func (f fixtureReads) CurrentSource(context.Context, *Object) (bool, error) { return true, nil }
func (f fixtureReads) AfterSaved(_ context.Context, _ string, _ *Object, m *Object, _ []*Object) (*Object, error) {
	if f.after != nil {
		return m, f.after(m)
	}
	return m, nil
}
func persistenceFixture(t *testing.T) (*batch, scopedFixture) {
	t.Helper()
	memory, f := serviceSetup(t)
	h, e := habitservice.New(context.Background(), f.main, f.recovery, habitservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	s, e := loadSchemas()
	if e != nil {
		t.Fatal(e)
	}
	r := &Runner{d: Dependencies{Memory: memory, Habits: h, Reads: fixtureReads{f: f}, Now: time.Now, Disposed: func() bool { return false }}, schemas: s}
	messages := []*Object{parseObject(t, `{"id":"m","sceneId":"s","senderId":"p","origin":"live","status":"available","parts":[{"type":"text","text":"I like tools"}],"sentAt":"2026-10-28T12:00:00.000Z","receivedAt":"2026-10-28T12:00:00.000Z"}`)}
	evidence, e := sourceadmission.TrustedEvidence(messages, func(string, string) (bool, error) { return false, nil })
	if e != nil {
		t.Fatal(e)
	}
	return &batch{r: r, ctx: context.Background(), scene: "s", messages: messages, evidence: evidence}, f
}
func sourceCall(t *testing.T, content string) call {
	return call{"learning.propose", parseObject(t, fmt.Sprintf(`{"content":%q,"kind":"fact","sourceMessageIds":["m"],"subject":{"kind":"self","sourceMessageId":"m"},"epistemicKind":"explicit"}`, content))}
}
func integrity(t *testing.T, f scopedFixture) {
	t.Helper()
	var result string
	if e := f.main.QueryRow("PRAGMA integrity_check").Scan(&result); e != nil || result != "ok" {
		t.Fatalf("integrity %s %v", result, e)
	}
	rows, e := f.main.Query("PRAGMA foreign_key_check")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}
func TestPersistenceActualWriterSavedRejectedDuplicate(t *testing.T) {
	b, f := persistenceFixture(t)
	p := new(Progress)
	calls := []call{{"learning.propose", obj("invalidReference", true)}, sourceCall(t, "Synthetic fact"), sourceCall(t, "Synthetic fact")}
	ok, e := b.persist(calls, p)
	if e != nil || !ok {
		t.Fatalf("run %v %v", ok, e)
	}
	if !p.Applying || p.WritePending || p.Totals.CandidateCount != 3 || p.Totals.RejectedCount != 1 || p.Totals.SavedCount != 2 || p.Totals.IgnoredCount != 0 {
		t.Fatalf("progress %+v", p)
	}
	var n int
	if e = f.main.QueryRow("SELECT count(*) FROM memories").Scan(&n); e != nil || n != 1 {
		t.Fatalf("stored %d %v", n, e)
	}
	integrity(t, f)
}
func TestPersistencePostCommitFailureRetainsIntentAndDurableWrite(t *testing.T) {
	b, f := persistenceFixture(t)
	b.r.d.Reads = fixtureReads{f: f, after: func(*Object) error { return errors.New("synthetic_index_failure") }}
	p := new(Progress)
	ok, e := b.persist([]call{sourceCall(t, "Synthetic partial")}, p)
	if e == nil || ok || !p.Applying || !p.WritePending || p.Totals.SavedCount != 0 {
		t.Fatalf("partial %v %v %+v", ok, e, p)
	}
	var n int
	if e = f.main.QueryRow("SELECT count(*) FROM memories").Scan(&n); e != nil || n != 1 {
		t.Fatalf("commit missing %d %v", n, e)
	}
	integrity(t, f)
}
func TestPersistenceRejectedEffectStillBlocksReplay(t *testing.T) {
	b, f := persistenceFixture(t)
	c := sourceCall(t, "Synthetic rejected")
	_ = c.Arguments.Set("sourceMessageIds", []any{"missing"})
	p := new(Progress)
	ok, e := b.persist([]call{c}, p)
	if e != nil || !ok || !p.Applying || p.WritePending || p.Totals.RejectedCount != 1 || p.Totals.SavedCount != 0 {
		t.Fatalf("rejected %v %v %+v", ok, e, p)
	}
	integrity(t, f)
}

func TestPersistenceWriterFailureRollsBackWithoutChangingAuthorityACK(t *testing.T) {
	b, f := persistenceFixture(t)
	p := new(Progress)
	if ok, e := b.persist([]call{sourceCall(t, "Synthetic initial")}, p); e != nil || !ok {
		t.Fatalf("seed %v", e)
	}
	before, e := recovery.ReadGate(context.Background(), f.main)
	if e != nil {
		t.Fatal(e)
	}
	head, e := f.recovery.Head(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.main.Exec(`CREATE TRIGGER learning_failure BEFORE INSERT ON memories WHEN NEW.content='Synthetic failing' BEGIN SELECT RAISE(ABORT,'synthetic_write_failure'); END`); e != nil {
		t.Fatal(e)
	}
	p = new(Progress)
	ok, e := b.persist([]call{sourceCall(t, "Synthetic failing")}, p)
	if e == nil || ok || !p.Applying || !p.WritePending || p.Totals.SavedCount != 0 {
		t.Fatalf("failure %v %v %+v", ok, e, p)
	}
	after, e := recovery.ReadGate(context.Background(), f.main)
	if e != nil {
		t.Fatal(e)
	}
	now, e := f.recovery.Head(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if before.Head != after.Head || head != now {
		t.Fatal("rollback changed authority ACK")
	}
	var n int
	if e = f.main.QueryRow("SELECT count(*) FROM memories").Scan(&n); e != nil || n != 1 {
		t.Fatalf("rollback lost initial row: %d %v", n, e)
	}
	integrity(t, f)
	if e = f.recovery.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := recovery.OpenWriter(context.Background(), before.Authority)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	recovered, e := reopened.Head(context.Background())
	if e != nil || recovered != head {
		t.Fatalf("reopen head %v %v", recovered, e)
	}
}

// Both source writers return saved for duplicate proposals. Habit evidence is
// deduplicated, but that is not an ignored result and must not lower savedCount.
func TestPersistenceActualHabitDuplicateCountsSettledWrites(t *testing.T) {
	b, f := persistenceFixture(t)
	h, e := habitservice.New(context.Background(), f.main, f.recovery, habitservice.Options{Now: func(*habitservice.Service) float64 { return 1793188800000 }})
	if e != nil {
		t.Fatal(e)
	}
	b.r.d.Habits = h
	_ = b.messages[0].Set("parts", []any{obj("type", "text", "text", "assistant always use concise replies")})
	c := call{"habits.propose", obj("kind", "botExpression", "preference", "concise", "direction", float64(1), "sourceMessageIds", []any{"m"})}
	p := new(Progress)
	ok, e := b.persist([]call{c, c}, p)
	if e != nil || !ok || p.WritePending || !p.Applying || p.Totals.SavedCount != 2 || p.Totals.RejectedCount != 0 || p.Totals.IgnoredCount != 0 {
		t.Fatalf("habit duplicate %v %v %+v", ok, e, p)
	}
	var n int
	if e = f.main.QueryRow("SELECT count(*) FROM habits").Scan(&n); e != nil || n != 1 {
		t.Fatalf("habit rows %d %v", n, e)
	}
	integrity(t, f)
}

func TestPersistenceSettledSaveCancellationDoesNotRevokeReadTicket(t *testing.T) {
	b, f := persistenceFixture(t)
	policy, e := modelread.New(f.store)
	if e != nil {
		t.Fatal(e)
	}
	b.r.d.Policy = policy
	b.ticket, e = policy.Require(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.ctx = ctx
	b.r.d.Reads = fixtureReads{f: f, after: func(*Object) error { cancel(); return nil }}
	p := new(Progress)
	_, _ = b.persist([]call{sourceCall(t, "Synthetic cancelled save")}, p)
	if !p.Applying || p.WritePending || p.Totals.SavedCount != 1 {
		t.Fatalf("settled save became unknown: %+v", p)
	}
	integrity(t, f)
}
func TestPersistenceActualReadRevisionRevocationKeepsUnknownBarrier(t *testing.T) {
	b, f := persistenceFixture(t)
	policy, e := modelread.New(f.store)
	if e != nil {
		t.Fatal(e)
	}
	b.r.d.Policy = policy
	b.ticket, e = policy.Require(context.Background(), "s")
	if e != nil {
		t.Fatal(e)
	}
	b.r.d.Reads = fixtureReads{f: f, after: func(*Object) error {
		_, e := f.main.Exec("UPDATE scenes SET value=json_set(value,'$.memoryReadRevision',1) WHERE id='s'")
		return e
	}}
	p := new(Progress)
	ok, e := b.persist([]call{sourceCall(t, "Synthetic revoked save")}, p)
	if ok || e == nil || e.Error() != "memory_read_revoked" || !p.Applying || !p.WritePending || p.Totals.SavedCount != 0 {
		t.Fatalf("revoked ticket: %v %v %+v", ok, e, p)
	}
	integrity(t, f)
}

func TestProposalReparsesCurrentFeatureSettingsBeforeWrite(t *testing.T) {
	b, f := persistenceFixture(t)
	b.settings.GraphEnabled = true
	c := sourceCall(t, "Synthetic current settings")
	_ = c.Arguments.Set("relations", []any{})
	p := new(Progress)
	ok, e := b.persist([]call{c}, p)
	if e == nil || ok || !p.Applying || !p.WritePending || p.Totals.SavedCount != 0 {
		t.Fatalf("stale schema admitted: %v %v %+v", ok, e, p)
	}
	var n int
	if e = f.main.QueryRow("SELECT count(*) FROM memories").Scan(&n); e != nil || n != 0 {
		t.Fatalf("unexpected write %d %v", n, e)
	}
	integrity(t, f)
}
