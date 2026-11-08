package memoryservice

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionproposal"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Unlike command package recording hooks, this exercises the actual Service
// revision, relation, projection, working-set, knowledge and semantic owners.
func TestProposalActualOwnersCorrectionRollbackAndEligibilityACK(t *testing.T) {
	t.Run("superseded-parent-retains-historical-support", func(t *testing.T) { testProposalActualOwners(t, false) })
	t.Run("source-unavailable-refresh-emits-eligibility", func(t *testing.T) { testProposalActualOwners(t, true) })
}
func testProposalActualOwners(t *testing.T, unavailable bool) {
	s, f := serviceSetup(t)
	ctx := context.Background()
	if _, e := f.main.Exec(`UPDATE scenes SET value='{"id":"s","adapterId":"adapter","accountId":"account","learningEnabled":true}' WHERE id='s'`); e != nil {
		t.Fatal(e)
	}
	sequence := 0
	runtime := assertionproposal.Runtime{ID: func() (string, error) { sequence++; return fmt.Sprintf("proposal-owner-%d", sequence), nil }, Now: func() string { return "2026-10-28T12:00:00.000Z" }}
	input := func(content, msg string) (*Object, *Object) {
		return parseObject(t, fmt.Sprintf(`{"sceneId":"s","simulation":false,"provenance":"source-only","sourceEvidence":[{"sceneId":"s","messageId":%q,"senderId":"p","available":true,"isBot":false}]}`, msg)), parseObject(t, fmt.Sprintf(`{"content":%q,"kind":"fact","personId":"p","sources":[{"sceneId":"s","messageId":%q,"senderId":"p"}]}`, content, msg))
	}
	propose := func(in, c *Object) (assertionproposal.Result, error) {
		var out assertionproposal.Result
		e := s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
			var e error
			out, e = s.proposeAssertion(ctx, tx, in, c, runtime)
			return e
		})
		return out, e
	}
	in, c := input("Synthetic original", "m1")
	root, e := propose(in, c)
	if e != nil || root.Status != "saved" {
		t.Fatalf("root %v %v", root, e)
	}
	rootID := text(root.Memory, "id")
	refs, e := s.CaptureParentReferences(ctx, Context{SceneID: "s"}, []*Object{root.Memory})
	if e != nil || len(refs) != 1 {
		t.Fatalf("parent %v %v", refs, e)
	}
	in, c = input("Synthetic dependent", "m2")
	_ = in.Set("provenance", "contextual")
	_ = in.Set("dependencyMemoryIds", []any{rootID})
	reference := parseObject(t, fmt.Sprintf(`{"id":%q,"version":%d,"deletionEpoch":%d}`, refs[0].ID, refs[0].Version, refs[0].DeletionEpoch))
	_ = in.Set("dependencyParentReferences", []any{reference})
	child, e := propose(in, c)
	if e != nil || child.Status != "saved" {
		t.Fatalf("child %v %v", child, e)
	}
	childID := text(child.Memory, "id")
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if e := f.main.QueryRow(query, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	if got := count("SELECT count(*) FROM memory_fts WHERE assertion_id IN (?,?)", rootID, childID); got != 2 {
		t.Fatalf("real projection not populated: %d", got)
	}
	if count("SELECT count(*) FROM memory_person_projection_state WHERE assertion_id IN (?,?)", rootID, childID) != 2 {
		t.Fatal("actual person projection not populated")
	}
	if _, e = f.main.Exec(`INSERT INTO vector_spaces VALUES('proposal-space','{}')`); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{rootID, childID} {
		if _, e = f.main.Exec("INSERT INTO memory_vectors VALUES(?,?,?)", id, "proposal-space", []byte{1, 2, 3, 4}); e != nil {
			t.Fatal(e)
		}
	}
	gateBefore, e := recovery.ReadGate(ctx, f.main)
	if e != nil {
		t.Fatal(e)
	}
	headBefore, e := f.recovery.Head(ctx)
	if e != nil {
		t.Fatal(e)
	}
	revisionsBefore := count("SELECT count(*) FROM memory_assertion_revisions")
	if _, e = f.main.Exec(`CREATE TRIGGER proposal_failure BEFORE INSERT ON memories WHEN NEW.content='Synthetic failing correction' BEGIN SELECT RAISE(ABORT,'proposal_second_write'); END`); e != nil {
		t.Fatal(e)
	}
	in, c = input("Synthetic failing correction", "m3")
	_ = c.Set("supersedesId", rootID)
	if _, e = propose(in, c); e == nil || !strings.Contains(e.Error(), "proposal_second_write") {
		t.Fatalf("expected SQL failure %v", e)
	}
	if _, e = f.main.Exec("DROP TRIGGER proposal_failure"); e != nil {
		t.Fatal(e)
	}
	gateFailed, e := recovery.ReadGate(ctx, f.main)
	if e != nil {
		t.Fatal(e)
	}
	headFailed, e := f.recovery.Head(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if gateFailed.Head != gateBefore.Head || headFailed != headBefore {
		t.Fatal("failed proposal changed durable head/ACK")
	}
	if count("SELECT count(*) FROM memory_assertion_revisions") != revisionsBefore || count("SELECT count(*) FROM memory_vectors") != 2 || count("SELECT count(*) FROM memory_fts WHERE assertion_id IN (?,?)", rootID, childID) != 2 {
		t.Fatal("failed correction leaked history/vector/projection effects")
	}
	// Source-owned fixture setup, not a native recall facade implementation.
	if unavailable {
		if _, e = f.main.Exec("INSERT INTO recall_tombstones(scene_id,message_id,recalled_at) VALUES('s','m2','2026-10-28T12:00:00.000Z')"); e != nil {
			t.Fatal(e)
		}
	}
	in, c = input("Synthetic successful correction", "m4")
	_ = c.Set("supersedesId", rootID)
	corrected, e := propose(in, c)
	if e != nil || corrected.Status != "saved" {
		t.Fatalf("correction %v %v", corrected, e)
	}
	if e = f.store.Read(ctx, func(r *assertions.Reader) error {
		old, e := r.Record(rootID)
		if e != nil {
			return e
		}
		dependent, e := r.Record(childID)
		if e != nil {
			return e
		}
		wantStatus := "active"
		if unavailable {
			wantStatus = "invalidated"
		}
		if text(old, "status") != "superseded" || text(dependent, "status") != wantStatus {
			t.Fatalf("actual owner invalidation old=%v child=%v", old, dependent)
		}
		state, e := r.State(childID)
		if e != nil {
			return e
		}
		wantEpoch := int64(0)
		if unavailable {
			wantEpoch = 1
		}
		if state.DeletionEpoch != wantEpoch {
			t.Fatal("dependent history purge absent", state)
		}
		a, e := assertionauthority.New(r, s.d.Authority)
		if e != nil {
			return e
		}
		applicable, e := a.CurrentApplicable(dependent)
		if e != nil {
			return e
		}
		if applicable {
			t.Fatal("historical dependent incorrectly currently applicable")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	wantDependentProjection := 1
	if unavailable {
		wantDependentProjection = 0
	}
	if count("SELECT count(*) FROM memory_vectors WHERE memory_id IN (?,?)", rootID, childID) != wantDependentProjection || count("SELECT count(*) FROM memory_fts WHERE assertion_id=?", childID) != wantDependentProjection {
		t.Fatal("actual projection/vector owners retained unavailable assertions")
	}
	if count("SELECT count(*) FROM memory_person_projection_state WHERE assertion_id=?", childID) != wantDependentProjection {
		t.Fatal("actual person projection retained unavailable records")
	}
	// Source projections retain supported superseded history; only the invalidated
	// dependent becomes unavailable. Superseded vectors are still removed.
	if count("SELECT count(*) FROM memory_fts WHERE assertion_id=?", rootID) != 1 || count("SELECT count(*) FROM memory_person_projection_state WHERE assertion_id=?", rootID) != 1 {
		t.Fatal("supported superseded projection incorrectly removed")
	}
	if count("SELECT count(*) FROM memory_fts WHERE assertion_id=?", text(corrected.Memory, "id")) != 1 {
		t.Fatal("new correction not projected after commit")
	}
	gate, e := recovery.ReadGate(ctx, f.main)
	if e != nil {
		t.Fatal(e)
	}
	head, e := f.recovery.Head(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if gate.Head != head || (unavailable && (head.Sequence <= headBefore.Sequence || gate.MinimumReader < 11)) || (!unavailable && head != headBefore) {
		t.Fatalf("durable eligibility ACK missing gate=%+v head=%+v", gate, head)
	}
	ledger, e := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	var eligibility int
	e = ledger.QueryRow("SELECT count(*) FROM revocations WHERE json_extract(payload,'$.kind')='assertion_eligibility'").Scan(&eligibility)
	closeErr := ledger.Close()
	wantEligibility := 0
	if unavailable {
		wantEligibility = 1
	}
	if e != nil || closeErr != nil || eligibility != wantEligibility {
		t.Fatalf("missing durable eligibility intent: %d %v %v", eligibility, e, closeErr)
	}
	// The gate's exact signed head is the real main-database ACK, not a fabricated
	// memory_recovery_intents table. Recovery.Publish above verifies its authority.
	raw, e := jsjson.Stringify(corrected.Memory)
	if e != nil || strings.Contains(raw, "dependencyParentReferences") {
		t.Fatalf("unexpected returned body %s %v", raw, e)
	}
}
