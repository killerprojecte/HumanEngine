package memoryservice

import (
	"context"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitmentadmission"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"testing"
)

// Source onPlanCommitted batches 41 distinct assertion dependencies as 40 + 1;
// it does not reject or truncate them at the mutation hook's per-call policy.
func TestCommitmentActualWorkspaceFortyPlusOneDependencies(t *testing.T) {
	s, f := serviceSetup(t)
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	_, e := f.main.Exec(`UPDATE scenes SET value='{"id":"s","adapterId":"adapter","accountId":"account","learningEnabled":true}' WHERE id='s'`)
	must(e)
	must(s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		for i := 0; i < 41; i++ {
			id := fmt.Sprintf("batch-%d", i)
			record := parseObject(t, fmt.Sprintf(`{"id":%q,"scope":{"kind":"scene","sceneId":"s","personId":"a"},"content":"Synthetic batch declaration","kind":"fact","epistemicKind":"explicit","sources":[{"sceneId":"s","messageId":%q,"senderId":"a"}],"importance":"low","confidence":0.5,"learnedAt":"2026-10-28T12:00:00.000Z","status":"active","locked":false,"origin":"learned","lineageStatus":"verified"}`, id, id))
			_ = record.Set("content", "Synthetic batch declaration "+id)
			proof := parseObject(t, fmt.Sprintf(`{"sources":[{"sceneId":"s","messageId":%q,"senderId":"a"}],"parents":[],"parentReferences":[],"subjectWitness":{"kind":"self","personId":"a","sourceMessageId":%q}}`, id, id))
			if e := tx.Repository.Write(record); e != nil {
				return e
			}
			if e := tx.Repository.SaveSupports(id, []any{proof}); e != nil {
				return e
			}
		}
		return nil
	}))
	options := commitmentadmission.Options{Authority: s.d.Authority, Enabled: true, ID: func() (string, error) { return "batch-plan", nil }}
	backing := parseObject(t, `{"id":"batch-0","version":1,"deletionEpoch":0}`)
	call := func(input, command *Object) error {
		return s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
			out, e := commitmentadmission.AdmitInAssertionScope(ctx, s.d.Commitments, tx.Store, s.d.WorkingSet, options, input, backing, command)
			if e == nil && out.Status != "saved" {
				t.Fatalf("batch command rejected: %+v", out)
			}
			return e
		})
	}
	must(call(parseObject(t, `{"sceneId":"s","simulation":false,"provenance":"source-only"}`), parseObject(t, `{"kind":"propose","terms":{"kind":"plan","title":"Synthetic batch plan","participantIds":["a"],"temporalScope":"future","polarity":"positive","modality":"asserted","conditions":[]}}`)))
	// Fixture augmentation retains exact actual assertion fingerprints/proofs; it
	// does not supply fabricated authority callbacks to the production adapter.
	evidence := []any{}
	must(s.d.Commitments.ReadCurrent(ctx, f.store, s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
		for i := 1; i < 41; i++ {
			id := fmt.Sprintf("batch-%d", i)
			view, e := read.CurrentAssertion(commitments.Context{SceneID: "s"}, commitments.Reference{ID: id, Version: 1})
			if e != nil {
				return e
			}
			if view == nil {
				t.Fatal("missing real backing")
			}
			hash, e := commitments.BackingFingerprint(view.Record)
			if e != nil {
				return e
			}
			ev := parseObject(t, fmt.Sprintf(`{"backing":{"id":%q,"version":1,"deletionEpoch":0},"backingFingerprint":%q,"kind":"proposal"}`, id, hash))
			_ = ev.Set("proofs", view.Proofs)
			evidence = append(evidence, ev)
		}
		return nil
	}))
	must(s.d.Commitments.Transaction(ctx, func(st *commitments.Store) error {
		return st.Read(ctx, func(r *commitments.Reader) error {
			head, e := r.Head("batch-plan")
			if e != nil {
				return e
			}
			revision, e := r.Revision("batch-plan", 1)
			if e != nil {
				return e
			}
			prior, _ := value(revision, "evidence").([]any)
			_ = revision.Set("evidence", append(prior, evidence...))
			return st.Put(head, revision)
		})
	}))
	_, e = f.main.Exec("DELETE FROM memory_working_set_jobs")
	must(e)
	must(call(parseObject(t, `{"sceneId":"s","simulation":false,"provenance":"contextual","exposedRefs":[{"id":"batch-plan","version":1,"deletionEpoch":0,"mutationVersion":1}]}`), parseObject(t, `{"kind":"confirm","target":{"id":"batch-plan","version":1,"deletionEpoch":0}}`)))
	var assertions, plans int
	must(f.main.QueryRow(`SELECT count(*) FROM memory_working_set_jobs WHERE json_extract(value,'$.snapshot.ref.kind')='assertion'`).Scan(&assertions))
	must(f.main.QueryRow(`SELECT count(*) FROM memory_working_set_jobs WHERE json_extract(value,'$.snapshot.ref.kind')='commitment'`).Scan(&plans))
	if assertions != 41 || plans != 1 {
		t.Fatalf("source 40+1 batching lost work: assertions=%d plans=%d", assertions, plans)
	}
}
