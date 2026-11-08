package memoryservice

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

// Real SQL + assertion provenance + knowledge projection + episode grouping,
// one connection, with no test replacement for the mandatory episode owner.
func TestSemanticEpisodeConcreteComposition(t *testing.T) {
	t.Run("original-declarations", func(t *testing.T) { testSemanticEpisodeConcreteComposition(t, false) })
	t.Run("internal-marker", func(t *testing.T) { testSemanticEpisodeConcreteComposition(t, true) })
}
func testSemanticEpisodeConcreteComposition(t *testing.T, marker bool) {
	s, f := serviceSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	records := []*Object{}
	views := []*Object{}
	for _, id := range []string{"a", "b"} {
		record := putRecord(t, f, id, `,"lineageStatus":"verified"`)
		source := epObj("sceneId", "s", "messageId", "source-"+id, "senderId", "person-"+id)
		_ = record.Set("sources", []any{source})
		_ = object(value(record, "scope")).Set("personId", "person-"+id)
		raw, e := jsjson.Stringify(record)
		must(e)
		_, e = f.main.Exec("UPDATE memories SET value=?, person_id=? WHERE id=?", raw, "person-"+id, id)
		must(e)
		supports := []any{epObj("sources", []any{source}, "parents", []any{}, "parentReferences", []any{}, "subjectWitness", epObj("kind", "self", "personId", "person-"+id, "sourceMessageId", "source-"+id))}
		raw, e = jsjson.Stringify(supports)
		must(e)
		_, e = f.main.Exec("INSERT INTO memory_supports(memory_id,value) VALUES(?,?)", id, raw)
		must(e)
		records = append(records, record)
	}
	refs, e := s.CaptureParentReferences(ctx, Context{SceneID: "s"}, records)
	must(e)
	if len(refs) != 2 {
		t.Fatal("could not capture original declarations")
	}
	for _, r := range refs {
		v, e := s.SemanticCurrent(ctx, Context{SceneID: "s"}, r)
		must(e)
		if v == nil {
			t.Fatal("current provenance missing")
		}
		views = append(views, v)
	}

	// The original declaration refs remain the only semantic parents. A real
	// internal marker has separate revision authority but no semantic declaration.
	trigger := refs[0]
	anchorRecord := object(value(views[0], "record"))
	anchorProofs := epArr(value(views[0], "proofs"))
	if marker {
		record := anchorRecord.Clone()
		_ = record.Set("id", "marker")
		_ = record.Set("internalUse", "knowledge_declaration")
		support := object(anchorProofs[0]).Clone()
		support.Delete("proofId")
		must(s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
			if e := tx.Repository.Write(record); e != nil {
				return e
			}
			return tx.Repository.SaveSupports("marker", []any{support})
		}))
		trigger = Reference{ID: "marker", Version: 1}
		must(s.d.Commitments.ReadCurrent(ctx, s.d.Assertions, s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
			view, e := read.CurrentAssertion(commitments.Context{SceneID: "s"}, semanticRef(refObject(trigger)))
			if e != nil {
				return e
			}
			if view == nil {
				t.Fatal("missing real internal marker authority")
			}
			anchorRecord, anchorProofs = view.Record, view.Proofs
			return nil
		}))
		current, e := s.SemanticCurrent(ctx, Context{SceneID: "s"}, trigger)
		must(e)
		if current != nil {
			t.Fatal("marker became semantic declaration")
		}
	}
	entity := epObj("id", "event", "sceneId", "s", "kind", "event", "title", "Synthetic event", "backing", refObject(trigger), "originEdgeId", "edge-a")
	must(s.d.Knowledge.Storage.Transaction(ctx, func(store *knowledge.Store) error {
		for i, r := range refs {
			view := views[i]
			record := object(value(view, "record"))
			fp, e := knowledge.BackingFingerprint(record)
			if e != nil {
				return e
			}
			proofs := epArr(value(view, "proofs"))
			source := epArr(epGet(proofs[0], "sources"))[0]
			root, e := semanticHash([]any{"s", epGet(source, "messageId")})
			if e != nil {
				return e
			}
			deps := []any{}
			if i > 0 {
				firstFP, e := knowledge.BackingFingerprint(anchorRecord)
				if e != nil {
					return e
				}
				deps = append(deps, epObj("entity", entity, "backingFingerprint", firstFP, "proofs", anchorProofs))
			}

			backing := refObject(r)
			readDependencies := []any{}
			if marker && i == 0 {
				readDependencies = append(readDependencies, epObj("backing", backing, "backingFingerprint", fp, "proofs", proofs))
				backing = refObject(trigger)
				fp, e = knowledge.BackingFingerprint(anchorRecord)
				if e != nil {
					return e
				}
				proofs = anchorProofs
			}
			edge := epObj("id", "edge-"+r.ID, "version", float64(1), "sceneId", "s", "registryVersion", float64(1), "predicate", "participates_in", "subject", epObj("kind", "person", "personId", "person-"+r.ID), "object", epObj("kind", "entity", "entity", entity), "polarity", "positive", "modality", "asserted", "temporalScope", "past", "conditions", []any{}, "backing", backing, "backingFingerprint", fp, "proofs", proofs, "spans", []any{epObj("source", source, "rootId", root, "partIndex", float64(0), "start", float64(0), "end", float64(1), "spanHash", strings.Repeat("a", 64), "stance", "self")}, "dependencies", deps, "readDependencies", readDependencies, "status", "candidate", "admission", "qualified_candidate", "admissionPolicyVersion", float64(1), "createdAt", "2026-10-28T00:00:00.000Z")
			if e = store.Put(edge, "episode-native-"+r.ID); e != nil {
				return e
			}
		}
		return nil
	}))
	options := knowledge.ReadOptions{KnowledgeEnabled: true, CommitmentEnabled: true, Authority: s.d.Authority}
	assertGroup := func(result *Object) {
		t.Helper()
		groups := epArr(value(result, "groups"))
		if len(groups) != 1 || len(epArr(epGet(groups[0], "parents"))) != 2 || text(result, "coverage") != "complete" {
			raw, _ := jsjson.Stringify(result)
			t.Fatal("missing real episode", raw)
		}
		for _, p := range epArr(epGet(groups[0], "parents")) {
			if epGet(epGet(p, "ref"), "id") == "marker" {
				t.Fatal("internal marker leaked into parents")
			}
		}
		if len(epArr(epGet(groups[0], "anchorQualifications"))) != 2 {
			t.Fatal("missing qualification closure")
		}
	}
	result, e := s.SemanticEpisodes(ctx, Context{SceneID: "s"}, trigger, options)
	must(e)
	assertGroup(result)
	must(s.d.WorkingSet.Transaction(ctx, func(bound *workingset.Service) error {
		return bound.ReadScope(ctx, func(scope *workingset.Scope) error {
			return s.SemanticEpisodesInScope(ctx, scope, Context{SceneID: "s"}, trigger, options, func(result *Object) error { assertGroup(result); return nil })
		})
	}))
	// Exercise the actual notification groups adapter, not an empty placeholder.
	stack := []func(){}
	events := []*Object{}
	queue, e := NewNotificationQueue(NotificationPorts{Enabled: func(string) bool { return true }, PolicyIdentity: func(string) string { return "fixed" }, Current: func(scene string, r Reference) (*Object, error) {
		return s.SemanticCurrent(ctx, Context{SceneID: scene}, r)
	}, Episodes: func(scene string, r Reference) ([]*Object, error) {
		return s.SemanticEpisodeGroups(ctx, Context{SceneID: scene}, r, options)
	}, PostStack: func(fn func()) { stack = append(stack, fn) }, Failed: func() { t.Fatal("notification failure") }})
	must(e)
	_, e = queue.Subscribe(func(event *Object) error { events = append(events, event); return nil })
	must(e)
	must(queue.AssertionCommitted("s", trigger))
	for _, fn := range stack {
		fn()
	}
	expectedEvents := 2
	if marker {
		expectedEvents = 1
	}
	if len(events) != expectedEvents || epGet(value(events[len(events)-1], "episodeAnchor"), "id") != "event" {
		t.Fatal("missing assertion and episode notifications")
	}
	// Exact-reference staleness and simulation do not synthesize groups.
	stale := trigger
	stale.Version++
	result, e = s.SemanticEpisodes(ctx, Context{SceneID: "s"}, stale, options)
	must(e)
	if len(epArr(value(result, "groups"))) != 0 {
		t.Fatal("stale group")
	}
	result, e = s.SemanticEpisodes(ctx, Context{SceneID: "s", Simulation: true}, trigger, options)
	must(e)
	if len(epArr(value(result, "groups"))) != 0 {
		t.Fatal("simulation group")
	}
	// Revoke one original declaration's proof through the real revision owner.
	// Even an independently current internal marker cannot retain this group.
	must(s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error { return tx.Repository.SaveSupports("b", []any{}) }))
	result, e = s.SemanticEpisodes(ctx, Context{SceneID: "s"}, trigger, options)
	must(e)
	if len(epArr(value(result, "groups"))) != 0 {
		t.Fatal("proof-revoked declaration still exposed as an episode")
	}

}
