package memoryservice

import (
	"context"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitmentadmission"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"strings"
	"testing"
)

func TestCommitmentCommandsActualOwners(t *testing.T) {
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
	// Two real self declarations, retained by the actual assertion coordinator.
	for _, person := range []string{"a", "b"} {
		record := parseObject(t, fmt.Sprintf(`{"id":%q,"scope":{"kind":"scene","sceneId":"s","personId":%q},"content":"Synthetic commitment declaration","kind":"fact","epistemicKind":"explicit","sources":[{"sceneId":"s","messageId":%q,"senderId":%q}],"importance":"low","confidence":0.5,"learnedAt":"2026-10-28T12:00:00.000Z","status":"active","locked":false,"origin":"learned","lineageStatus":"verified"}`, person, person, person, person))
		proof := parseObject(t, fmt.Sprintf(`{"sources":[{"sceneId":"s","messageId":%q,"senderId":%q}],"parents":[],"parentReferences":[],"subjectWitness":{"kind":"self","personId":%q,"sourceMessageId":%q}}`, person, person, person, person))
		must(s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
			if e := tx.Repository.Write(record); e != nil {
				return e
			}
			return tx.Repository.SaveSupports(person, []any{proof})
		}))
	}
	terms := `{"kind":"plan","title":"Synthetic joint plan","participantIds":["a","b"],"temporalScope":"future","polarity":"positive","modality":"asserted","conditions":[]}`
	options := commitmentadmission.Options{Authority: s.d.Authority, Enabled: true, ID: func() (string, error) { return "joint-plan", nil }, Dirty: s.d.Dirty}
	call := func(person, command string, version, mutation int, expose bool) (out commitments.CommandResult, err error) {
		input := parseObject(t, fmt.Sprintf(`{"sceneId":"s","simulation":false,"provenance":"contextual","sourceEvidence":[{"sceneId":"s","messageId":%q,"senderId":%q,"available":true,"isBot":false,"mentions":[{"userId":"b","isBot":false},{"userId":"a","isBot":false}]}]}`, person, person))
		if expose {
			_ = input.Set("exposedRefs", []any{parseObject(t, fmt.Sprintf(`{"id":"joint-plan","version":%d,"deletionEpoch":0,"mutationVersion":%d}`, version, mutation))})
		}
		backing := parseObject(t, fmt.Sprintf(`{"id":%q,"version":1,"deletionEpoch":0}`, person))
		raw := parseObject(t, command)
		err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
			var e error
			out, e = commitmentadmission.AdmitInAssertionScope(ctx, s.d.Commitments, tx.Store, s.d.WorkingSet, options, input, backing, raw)
			return e
		})
		return
	}
	check := func(out commitments.CommandResult, e error, status, confirmation, lifecycle string) {
		t.Helper()
		must(e)
		if out.Status != status || (confirmation != "" && text(out.Item, "confirmation") != confirmation) || (lifecycle != "" && text(out.Item, "lifecycle") != lifecycle) {
			t.Fatalf("unexpected command result %+v", out)
		}
	}
	propose := `{"kind":"propose","terms":` + terms + `}`
	out, e := call("a", propose, 0, 0, false)
	check(out, e, "saved", "proposed", "pending")
	before, e := f.recovery.Head(ctx)
	must(e)
	out, e = call("a", propose, 0, 0, false)
	check(out, e, "duplicate", "proposed", "pending")
	after, e := f.recovery.Head(ctx)
	must(e)
	if after != before {
		t.Fatal("duplicate journaled intent")
	}
	target := func(kind string, v int) string {
		return fmt.Sprintf(`{"kind":%q,"target":{"id":"joint-plan","version":%d,"deletionEpoch":0}}`, kind, v)
	}
	out, e = call("b", target("confirm", 1), 1, 99, true)
	must(e)
	if out.Reason != "commitment_snapshot_changed" {
		t.Fatal(out)
	}
	out, e = call("b", target("confirm", 1), 1, 1, true)
	check(out, e, "saved", "confirmed", "pending")
	out, e = call("a", target("complete", 1), 1, 2, true)
	check(out, e, "saved", "confirmed", "pending")
	out, e = call("b", target("complete", 1), 1, 3, true)
	check(out, e, "saved", "confirmed", "done")
	amend := `{"kind":"amend","target":{"id":"joint-plan","version":1,"deletionEpoch":0},"terms":` + strings.Replace(terms, "joint plan", "amended plan", 1) + `}`
	out, e = call("a", amend, 1, 4, true)
	check(out, e, "saved", "proposed", "pending")
	out, e = call("b", target("confirm", 2), 2, 5, true)
	check(out, e, "saved", "confirmed", "pending")
	out, e = call("a", target("cancel", 2), 2, 6, true)
	check(out, e, "saved", "confirmed", "cancelled")
	must(s.d.Commitments.Read(ctx, func(r *commitments.Reader) error {
		snapshot, e := r.Snapshot("joint-plan")
		if e != nil {
			return e
		}
		timeline := object(value(snapshot, "timeline"))
		rows, _ := value(timeline, "transitions").([]any)
		if len(rows) != 7 {
			t.Fatalf("normal timeline count %d", len(rows))
		}
		for _, row := range rows {
			if value(object(row), "states") == nil {
				t.Fatal("normal command wrote privacy-only states")
			}
		}
		revisions, _ := value(snapshot, "revisions").([]any)
		if len(revisions) != 2 {
			t.Fatal("immutable terms history lost")
		}
		return nil
	}))

	// Requalify the recorded normal states through the actual historical assertion
	// authority and temporal reader on the same owner, not a nonnull JSON check.
	must(s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.WithWriteSession(func(session *recovery.WriteSession) error {
			return tx.Store.Read(ctx, func(ar *assertions.Reader) error {
				return ar.WithSupportReadStatements(func(ar *assertions.Reader) error {
					authority, e := assertionauthority.New(ar, s.d.Authority)
					if e != nil {
						return e
					}
					return s.d.Commitments.WithSession(session).Read(ctx, func(r *commitments.Reader) error {
						timeline, e := r.Timeline("joint-plan")
						if e != nil {
							return e
						}
						result, e := commitments.ReadTimeline(commitments.Context{SceneID: "s"}, commitments.Exposure{Reference: commitments.Reference{ID: "joint-plan", Version: 2}, MutationVersion: 7}, timeline, commitments.TimelineQuery{}, commitments.TimelinePorts{Revision: func(v float64) (*Object, error) { return r.Revision("joint-plan", v) }, Historical: func(c commitments.Context, ref commitments.Reference) (*commitments.Assertion, error) {
							return authority.HistoricalAssertion(assertionauthority.Context{SceneID: c.SceneID}, ref)
						}})
						if e != nil {
							return e
						}
						rows, _ := value(result, "recordedStates").([]any)
						if len(rows) != 7 {
							t.Fatalf("temporal reader normal states unavailable: %+v", result)
						}
						seen := map[string]bool{}
						for _, raw := range rows {
							row := object(raw)
							if text(row, "status") != "available" {
								t.Fatal("normal state unexpectedly redacted")
							}
							states, _ := value(row, "states").([]any)
							for _, v := range states {
								state := object(v)
								if value(state, "terms") == nil {
									t.Fatal("temporal reader lost terms")
								}
								seen[text(state, "lifecycle")] = true
							}
						}
						if !seen["done"] || !seen["cancelled"] || !seen["pending"] {
							t.Fatalf("temporal transitions lost: %v", seen)
						}
						return nil
					})
				})
			})
		})
	}))
	gate, e := recovery.ReadGate(ctx, f.main)
	must(e)
	head, e := f.recovery.Head(ctx)
	must(e)
	if head != gate.Head || gate.MinimumReader < 10 {
		t.Fatal("normal command head/ACK missing")
	}
	var jobs int
	must(f.main.QueryRow(`SELECT count(*) FROM memory_working_set_jobs WHERE json_extract(value,'$.snapshot.ref.kind')='commitment'`).Scan(&jobs))
	if jobs < 1 {
		t.Fatal("actual workspace owner did not enqueue plan")
	}
	// Fail after head, revision, timeline and actual working-set writes. Main
	// rollback retains the already journaled intent and must block publication.
	capture := func() []string {
		out := []string{}
		for _, table := range []string{"memory_commitment_heads", "memory_commitment_revisions", "memory_commitment_temporal_windows", "memory_commitment_transitions", "memory_working_set_jobs"} {
			var raw string
			must(f.main.QueryRow("SELECT coalesce(group_concat(value,'|'),'') FROM (SELECT value FROM " + table + " ORDER BY rowid)").Scan(&raw))
			out = append(out, raw)
		}
		return out
	}
	previous := capture()
	_, e = f.main.Exec(`CREATE TRIGGER fail_commitment_command BEFORE INSERT ON memory_commitment_commands BEGIN SELECT RAISE(ABORT,'synthetic_command_tail'); END`)
	must(e)
	_, e = call("a", target("complete", 2), 2, 7, true)
	if e == nil || !strings.Contains(e.Error(), "synthetic_command_tail") {
		t.Fatalf("partial SQL failure hidden: %v", e)
	}
	current := capture()
	for i := range previous {
		if previous[i] != current[i] {
			t.Fatalf("partial SQL rollback leaked table %d", i)
		}
	}
	failedHead, e := f.recovery.Head(ctx)
	must(e)
	failedGate, e := recovery.ReadGate(ctx, f.main)
	must(e)
	if failedHead.Sequence != head.Sequence+1 || failedGate.Head != head {
		t.Fatal("failed command lost durable intent or fabricated ACK")
	}
	if e = s.d.Commitments.Read(ctx, func(r *commitments.Reader) error { _, e := r.Head("joint-plan"); return e }); e == nil {
		t.Fatal("authority gap allowed publication")
	}

}
