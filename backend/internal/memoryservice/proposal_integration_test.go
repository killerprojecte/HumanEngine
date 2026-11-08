package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionproposal"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/relations"
	"github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"
	"strings"
	"testing"
)

func outerSetup(t *testing.T) (*Service, scopedFixture, ProposalOptions) {
	t.Helper()
	s, f := serviceSetup(t)
	if _, e := f.main.Exec(`UPDATE scenes SET value='{"id":"s","adapterId":"adapter","accountId":"account","learningEnabled":true}' WHERE id='s'`); e != nil {
		t.Fatal(e)
	}
	n := 0
	id := func() string { n++; return fmt.Sprintf("outer-%d", n) }
	return s, f, ProposalOptions{GraphEnabled: true, GraphAutoAdmission: true, KnowledgeEnabled: true, KnowledgeAutoAdmission: true, Runtime: assertionproposal.Runtime{ID: func() (string, error) { return id(), nil }, Now: func() string { return "2026-10-28T12:00:00.000Z" }}, RelationID: id, KnowledgeID: id, CommitmentID: func() (string, error) { return id(), nil }}
}
func outerInput(t *testing.T, msg string) (*Object, *Object) {
	t.Helper()
	in := parseObject(t, fmt.Sprintf(`{"sceneId":"s","simulation":false,"provenance":"source-only","sourceEvidence":[{"sceneId":"s","messageId":%q,"senderId":"p","available":true,"isBot":false,"sourceText":"I like tools","parts":[{"type":"text","text":"I like tools"}]}]}`, msg))
	c := parseObject(t, fmt.Sprintf(`{"content":%q,"kind":"fact","epistemicKind":"explicit","sources":[{"sceneId":"s","messageId":%q,"senderId":"p"}],"subject":{"kind":"self","sourceMessageId":%q}}`, "Synthetic "+msg, msg, msg))
	return in, c
}
func outerRelation(t *testing.T) *Object {
	return parseObject(t, `{"predicate":"participates_in","modality":"asserted","temporalScope":"current","personId":"p","target":{"kind":"new","entityKind":"plan","title":"Synthetic"},"polarity":"positive","conditions":[]}`)
}
func outerKnowledge(t *testing.T, msg string) *Object {
	return parseObject(t, fmt.Sprintf(`{"registryVersion":1,"predicate":"prefers_topic","subject":{"kind":"self","personId":"p"},"object":{"kind":"new","entityKind":"topic","title":"tools"},"polarity":"positive","modality":"asserted","temporalScope":"current","conditions":[],"sourceSpans":[{"messageId":%q,"partIndex":0,"start":7,"end":12,"stance":"self"}]}`, msg))
}
func outerCommand(t *testing.T) *Object {
	return parseObject(t, `{"kind":"propose","terms":{"kind":"plan","title":"Synthetic plan","participantIds":["p"],"temporalScope":"future","polarity":"positive","modality":"asserted","conditions":[]}}`)
}
func outerCount(t *testing.T, f scopedFixture, query string, args ...any) int {
	t.Helper()
	var n int
	if e := f.main.QueryRow(query, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func requireOuterSaved(t *testing.T, r ProposalResult, e error) {
	t.Helper()
	if e != nil || r.Status != "saved" {
		t.Fatalf("proposal %+v %v", r, e)
	}
}
func outerRef(t *testing.T, f scopedFixture, r ProposalResult) *Object {
	t.Helper()
	var ref *Object
	if e := f.store.Read(context.Background(), func(rd *assertions.Reader) error {
		state, e := rd.State(text(r.Memory, "id"))
		if e != nil {
			return e
		}
		if state.Version < 1 || state.Deleted {
			t.Fatalf("missing history %+v", state)
		}
		ref = parseObject(t, fmt.Sprintf(`{"id":%q,"version":%d,"deletionEpoch":%d}`, text(r.Memory, "id"), state.Version, state.DeletionEpoch))
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return ref
}

func TestOuterProposalActualOwnersBranches(t *testing.T) {
	for _, kind := range []string{"ordinary", "relations", "commitment", "knowledge", "contextual-knowledge"} {
		t.Run(kind, func(t *testing.T) {
			s, f, opts := outerSetup(t)
			in, c := outerInput(t, "m")
			switch kind {
			case "relations":
				_ = c.Set("relations", []any{outerRelation(t)})
			case "commitment":
				_ = c.Set("commitment", outerCommand(t))
				_ = in.Set("provenance", "contextual")
				_ = in.Set("dependencyMemoryIds", []any{"unavailable-original-parent"})
			case "knowledge", "contextual-knowledge":
				_ = c.Set("knowledge", []any{outerKnowledge(t, "m")})
				if kind == "contextual-knowledge" {
					_ = in.Set("provenance", "contextual")
					_ = in.Set("dependencyMemoryIds", []any{})
					_ = in.Set("dependencyParentReferences", []any{})
				}
			}
			out, e := s.Propose(context.Background(), in, c, opts)
			requireOuterSaved(t, out, e)
			ref := outerRef(t, f, out)
			wire, e := json.Marshal(out)
			if e != nil {
				t.Fatal(e)
			}
			decoded, e := jsjson.ParseBytes(wire)
			if e != nil {
				t.Fatal(e)
			}
			body := object(decoded)
			if !deepEqual(value(body, "memory"), out.Memory) {
				t.Fatalf("serialized backing body lost: %s", wire)
			}
			if out.Relations != nil && !deepEqual(proposalArray(value(object(value(body, "relations")), "created"))[0], out.Relations.Created[0]) {
				t.Fatal("serialized relation body lost")
			}
			if out.Commitment != nil && !deepEqual(value(object(value(body, "commitment")), "item"), out.Commitment.Item) {
				t.Fatal("serialized commitment body lost")
			}
			if out.Knowledge != nil && !deepEqual(proposalArray(value(object(value(body, "knowledge")), "created"))[0], out.Knowledge.Created[0]) {
				t.Fatal("serialized graph body lost")
			}

			switch kind {
			case "ordinary":
				if out.Relations != nil || out.Commitment != nil || out.Knowledge != nil {
					t.Fatal("unexpected branch")
				}
			case "relations":
				if out.Relations == nil || len(out.Relations.Created) != 1 {
					t.Fatalf("relations %+v", out.Relations)
				}
				r := out.Relations.Created[0]
				if !deepEqual(value(r, "backing"), ref) || text(r, "status") != "accepted" {
					t.Fatal("relation exact checkpoint or settings lost")
				}
				if outerCount(t, f, "SELECT count(*) FROM memory_relation_candidates") != 1 {
					t.Fatal("relation not persisted")
				}
			case "commitment":
				if out.Commitment == nil || out.Commitment.Status != "saved" {
					t.Fatalf("commitment %+v", out.Commitment)
				}
				if !strings.HasPrefix(text(out.Memory, "content"), "Model-extracted plan declaration (") || text(out.Memory, "internalUse") != "commitment_declaration" {
					t.Fatal("declaration not rewritten")
				}
				if outerCount(t, f, "SELECT count(*) FROM memory_commitment_heads") != 1 {
					t.Fatal("commitment not persisted")
				}
				gate, e := recovery.ReadGate(context.Background(), f.main)
				if e != nil {
					t.Fatal(e)
				}
				head, e := f.recovery.Head(context.Background())
				if e != nil || gate.Head != head || head.Sequence < 1 {
					t.Fatalf("command ACK %+v %+v %v", gate, head, e)
				}
			case "knowledge", "contextual-knowledge":
				if out.Knowledge == nil || len(out.Knowledge.Created) != 1 {
					t.Fatalf("knowledge %+v", out.Knowledge)
				}
				if !deepEqual(value(out.Knowledge.Created[0], "backing"), ref) {
					t.Fatal("graph exact checkpoint lost")
				}
				if outerCount(t, f, "SELECT count(*) FROM memory_knowledge_edges") != 1 {
					t.Fatal("graph not persisted")
				}
				if kind == "contextual-knowledge" && text(out.Memory, "internalUse") != "knowledge_declaration" {
					t.Fatal("contextual declaration missing")
				}
			}
			if text(c, "content") != "Synthetic m" {
				t.Fatal("mutated caller candidate")
			}
		})
	}
}

func TestOuterProposalActualRejectionsKeepOnlyExpectedBacking(t *testing.T) {
	for _, kind := range []string{"commitment-explicit", "commitment-literal", "commitment-mixed", "knowledge-first", "typed-commitment", "typed-knowledge", "typed-relations", "disabled-relations", "disabled-knowledge", "disabled-commitment", "simulation"} {
		t.Run(kind, func(t *testing.T) {
			s, f, opts := outerSetup(t)
			in, c := outerInput(t, "m")
			reason := ""
			saved := false
			switch kind {
			case "commitment-explicit":
				_ = c.Set("commitment", outerCommand(t))
				_ = c.Set("epistemicKind", "inferred")
				reason = "commitment_explicit_declaration_required"
			case "commitment-literal":
				_ = c.Set("commitment", outerCommand(t))
				object(proposalArray(value(in, "sourceEvidence"))[0]).Delete("sourceText")
				reason = "commitment_literal_self_source_required"
			case "commitment-mixed":
				_ = c.Set("commitment", outerCommand(t))
				_ = c.Set("relations", []any{outerRelation(t)})
				reason = "commitment_mixed_write_denied"
			case "knowledge-first":
				_ = c.Set("commitment", outerCommand(t))
				_ = c.Set("epistemicKind", "inferred")
				_ = c.Set("knowledge", []any{outerKnowledge(t, "m")})
				reason = "knowledge_mixed_write_denied"
			case "typed-commitment":
				command := outerCommand(t)
				_ = object(value(command, "terms")).Set("participantIds", []any{"other"})
				_ = c.Set("commitment", command)
				saved = true
			case "disabled-commitment":
				_ = c.Set("commitment", outerCommand(t))
				s.d.CommitmentsEnabled = func(commitments.Context) (bool, error) { return false, nil }
				saved = true
			case "typed-knowledge":
				p := outerKnowledge(t, "m")
				span := object(proposalArray(value(p, "sourceSpans"))[0])
				_ = span.Set("end", 100.0)
				_ = c.Set("knowledge", []any{p})
				saved = true
			case "disabled-knowledge":
				_ = c.Set("knowledge", []any{outerKnowledge(t, "m")})
				opts.KnowledgeEnabled = false
				saved = true
			case "typed-relations":
				_ = c.Set("relations", []any{parseObject(t, `{}`)})
				saved = true
			case "disabled-relations":
				_ = c.Set("relations", []any{outerRelation(t)})
				opts.GraphEnabled = false
				saved = true
			case "simulation":
				_ = in.Set("simulation", true)
				reason = "simulation_does_not_learn"
			}
			out, e := s.Propose(context.Background(), in, c, opts)
			if e != nil {
				t.Fatal(e)
			}
			if saved {
				requireOuterSaved(t, out, nil)
				if out.Commitment != nil && out.Commitment.Status != "rejected" || out.Knowledge != nil && len(out.Knowledge.Rejected) != 1 || out.Relations != nil && len(out.Relations.Rejected) != 1 {
					t.Fatalf("typed rejection %+v", out)
				}
				outerRef(t, f, out)
			} else if out.Status != "rejected" || out.Reason != reason {
				t.Fatalf("rejection %+v want %s", out, reason)
			}
			want := 0
			if saved {
				want = 1
			}
			if outerCount(t, f, "SELECT count(*) FROM memories") != want {
				t.Fatal("backing persistence mismatch")
			}
		})
	}
}

func TestOuterProposalLateGraphFailureRollsBackAllOwners(t *testing.T) {
	for _, loss := range []bool{false, true} {
		t.Run(fmt.Sprint(loss), func(t *testing.T) {
			s, f, opts := outerSetup(t)
			ctx := context.Background()
			// Create the real graph schema through a successful native proposal.
			in, c := outerInput(t, "seed")
			_ = c.Set("knowledge", []any{outerKnowledge(t, "seed")})
			seed, e := s.Propose(ctx, in, c, opts)
			requireOuterSaved(t, seed, e)
			memoryBefore := outerCount(t, f, "SELECT count(*) FROM memories")
			historyBefore := outerCount(t, f, "SELECT count(*) FROM memory_assertion_revisions")
			projectionBefore := outerCount(t, f, "SELECT count(*) FROM memory_person_projection_state")
			head, e := f.recovery.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			gate, e := recovery.ReadGate(ctx, f.main)
			if e != nil {
				t.Fatal(e)
			}
			raise := "ABORT"
			if loss {
				raise = "ROLLBACK"
			}
			if _, e = f.main.Exec(fmt.Sprintf(`CREATE TRIGGER fail_outer_graph BEFORE INSERT ON memory_knowledge_edges WHEN (SELECT count(*) FROM memory_knowledge_edges)>=2 BEGIN SELECT RAISE(%s,'outer_graph_tail'); END`, raise)); e != nil {
				t.Fatal(e)
			}
			in, c = outerInput(t, "failing")
			p := outerKnowledge(t, "failing")
			p2 := outerKnowledge(t, "failing")
			_ = p2.Set("object", parseObject(t, `{"kind":"literal","valueType":"text","value":"different"}`))
			_ = c.Set("knowledge", []any{p, p2})
			if _, e = s.Propose(ctx, in, c, opts); e == nil || !strings.Contains(e.Error(), "outer_graph_tail") {
				t.Fatalf("late failure %v", e)
			}
			if outerCount(t, f, "SELECT count(*) FROM memories") != memoryBefore || outerCount(t, f, "SELECT count(*) FROM memory_assertion_revisions") != historyBefore || outerCount(t, f, "SELECT count(*) FROM memory_person_projection_state") != projectionBefore || outerCount(t, f, "SELECT count(*) FROM memory_knowledge_edges") != 1 {
				t.Fatal("outer rollback leaked backing/history/projection/graph")
			}
			failedHead, e := f.recovery.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			failedGate, e := recovery.ReadGate(ctx, f.main)
			if e != nil {
				t.Fatal(e)
			}
			if failedHead != head || failedGate.Head != gate.Head {
				t.Fatal("graph insert fabricated privacy intent or ACK")
			}
		})
	}
}

func TestOuterProposalCommitmentIntentSurvivesOuterRollbackWithoutACK(t *testing.T) {
	s, f, opts := outerSetup(t)
	ctx := context.Background()
	head, e := f.recovery.Head(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.main.Exec(`CREATE TRIGGER fail_outer_command BEFORE INSERT ON memory_commitment_commands BEGIN SELECT RAISE(ABORT,'outer_command_tail'); END`); e != nil {
		t.Fatal(e)
	}
	in, c := outerInput(t, "m")
	_ = c.Set("commitment", outerCommand(t))
	if _, e = s.Propose(ctx, in, c, opts); e == nil || !strings.Contains(e.Error(), "outer_command_tail") {
		t.Fatalf("command tail %v", e)
	}
	for _, table := range []string{"memories", "memory_assertion_revisions", "memory_commitment_heads", "memory_commitment_commands", "memory_person_projection_state"} {
		if outerCount(t, f, "SELECT count(*) FROM "+table) != 0 {
			t.Fatalf("outer rollback leaked %s", table)
		}
	}
	after, e := f.recovery.Head(ctx)
	if e != nil {
		t.Fatal(e)
	}
	gate, e := recovery.ReadGate(ctx, f.main)
	if e != nil {
		t.Fatal(e)
	}
	if after.Sequence != head.Sequence+1 || gate.Head != head {
		t.Fatalf("intent or ACK %+v %+v", after, gate.Head)
	}
	if e = s.d.Commitments.Read(ctx, func(r *commitments.Reader) error { _, e := r.Head("missing"); return e }); e == nil {
		t.Fatal("intent gap allowed publication")
	}
}

func TestOuterProposalOriginalKnowledgeExposureAndParents(t *testing.T) {
	s, f, opts := outerSetup(t)
	ctx := context.Background()
	in, c := outerInput(t, "seed")
	_ = c.Set("knowledge", []any{outerKnowledge(t, "seed")})
	seed, e := s.Propose(ctx, in, c, opts)
	requireOuterSaved(t, seed, e)
	edge := seed.Knowledge.Created[0]
	entity := object(value(object(value(edge, "object")), "entity"))
	ref := outerRef(t, f, seed)
	for _, variant := range []string{"exact", "stale-entity", "stale-edge", "stale-parent"} {
		in, c = outerInput(t, variant)
		_ = in.Set("provenance", "contextual")
		parent := ref.Clone()
		entityRef := ref.Clone()
		edgeRef := ref.Clone()
		switch variant {
		case "stale-entity":
			_ = entityRef.Set("version", 99.0)
		case "stale-edge":
			_ = edgeRef.Set("version", 99.0)
		case "stale-parent":
			_ = parent.Set("version", 99.0)
		}
		_ = in.Set("dependencyMemoryIds", []any{text(seed.Memory, "id")})
		_ = in.Set("dependencyParentReferences", []any{parent})
		exposure := parseObject(t, fmt.Sprintf(`{"entityId":%q}`, text(entity, "id")))
		_ = exposure.Set("backingRef", entityRef)
		_ = in.Set("exposedKnowledgeEntities", []any{exposure})
		snapshot := parseObject(t, fmt.Sprintf(`{"id":%q,"version":1}`, text(edge, "id")))
		_ = snapshot.Set("backing", edgeRef)
		_ = in.Set("exposedKnowledgeEdges", []any{snapshot})
		p := outerKnowledge(t, variant)
		_ = p.Set("object", parseObject(t, fmt.Sprintf(`{"kind":"existing","entityId":%q}`, text(entity, "id"))))
		_ = c.Set("knowledge", []any{p})
		out, e := s.Propose(ctx, in, c, opts)
		requireOuterSaved(t, out, e)
		if out.Knowledge == nil {
			t.Fatal("knowledge absent")
		}
		if variant != "exact" {
			if len(out.Knowledge.Rejected) != 1 {
				t.Fatalf("%s accepted stale context %+v", variant, out.Knowledge)
			}
			continue
		}
		if len(out.Knowledge.Created) != 1 {
			t.Fatalf("original exposure/parents lost %+v", out.Knowledge)
		}
		created := out.Knowledge.Created[0]
		reads := proposalArray(value(created, "readDependencies"))
		if len(reads) != 2 || !deepEqual(value(object(reads[0]), "backing"), ref) || text(object(reads[1]), "edgeId") != text(edge, "id") {
			t.Fatal("original exact dependencies/order lost")
		}
		if e = f.store.Read(ctx, func(r *assertions.Reader) error {
			proofs, e := r.Supports(text(out.Memory, "id"))
			if e != nil {
				return e
			}
			for _, p := range proposalArray(proofs) {
				if len(proposalArray(value(object(p), "parents"))) != 0 {
					t.Fatal("declaration backing inherited original parents")
				}
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestOuterProposalOriginalRelationExposure(t *testing.T) {
	s, f, opts := outerSetup(t)
	ctx := context.Background()
	in, c := outerInput(t, "seed")
	_ = c.Set("relations", []any{outerRelation(t)})
	seed, e := s.Propose(ctx, in, c, opts)
	requireOuterSaved(t, seed, e)
	target := object(value(seed.Relations.Created[0], "target"))
	ref := outerRef(t, f, seed)
	for _, stale := range []bool{false, true} {
		in, c = outerInput(t, fmt.Sprint(stale))
		_ = in.Set("provenance", "contextual")
		_ = in.Set("dependencyMemoryIds", []any{})
		_ = in.Set("dependencyParentReferences", []any{})
		exp := parseObject(t, fmt.Sprintf(`{"entityId":%q}`, text(target, "id")))
		r := ref.Clone()
		if stale {
			_ = r.Set("deletionEpoch", 1.0)
		}
		_ = exp.Set("backingRef", r)
		_ = in.Set("exposedEntities", []any{exp})
		p := outerRelation(t)
		_ = p.Set("target", parseObject(t, fmt.Sprintf(`{"kind":"existing","entityId":%q}`, text(target, "id"))))
		_ = c.Set("relations", []any{p})
		out, e := s.Propose(ctx, in, c, opts)
		requireOuterSaved(t, out, e)
		if stale {
			if len(out.Relations.Rejected) != 1 || out.Relations.Rejected[0].Reason != "relation_target_not_exposed" {
				t.Fatalf("stale relation %+v", out.Relations)
			}
		} else if len(out.Relations.Created) != 1 {
			t.Fatalf("original relation exposure lost %+v", out.Relations)
		}
	}
}

func TestOuterProposalOriginalCommitmentExposure(t *testing.T) {
	s, _, opts := outerSetup(t)
	ctx := context.Background()
	in, c := outerInput(t, "seed")
	_ = c.Set("commitment", outerCommand(t))
	seed, e := s.Propose(ctx, in, c, opts)
	requireOuterSaved(t, seed, e)
	item := seed.Commitment.Item
	ref := object(value(item, "ref"))
	for _, stale := range []bool{true, false} {
		in, c = outerInput(t, fmt.Sprint(stale))
		_ = in.Set("provenance", "contextual")
		exp := ref.Clone()
		_ = exp.Set("mutationVersion", value(item, "mutationVersion"))
		if stale {
			_ = exp.Set("mutationVersion", 99.0)
		}
		_ = in.Set("exposedCommitments", []any{exp})
		command := parseObject(t, `{"kind":"cancel"}`)
		_ = command.Set("target", ref)
		_ = c.Set("commitment", command)
		out, e := s.Propose(ctx, in, c, opts)
		requireOuterSaved(t, out, e)
		if stale {
			if out.Commitment.Reason != "commitment_snapshot_changed" {
				t.Fatalf("stale exposure %+v", out.Commitment)
			}
		} else if out.Commitment.Status != "saved" {
			t.Fatalf("original commitment exposure lost %+v", out.Commitment)
		}
	}
}

func TestOuterRelationAdapterRejectsWrongOrExpiredOwner(t *testing.T) {
	s, _, _ := outerSetup(t)
	other, _, _ := outerSetup(t)
	ctx := context.Background()
	on := func(relations.Context) (bool, error) { return true, nil }
	policy := relations.Options{Authority: s.d.Authority, Enabled: on, AutoAdmission: on, LearningEnabled: on}
	ref := parseObject(t, `{"id":"missing","version":1,"deletionEpoch":0}`)
	var held *relationstorage.Store
	if e := s.revisions.Transaction(ctx, func(parent *assertions.RevisionTxn) error {
		return parent.Store.WithWriteSession(func(session *recovery.WriteSession) error {
			held = s.d.Relations.WithSession(session)
			if _, e := relations.AdmitInAssertionScope(ctx, other.d.Relations, parent.Store, policy, relations.Context{SceneID: "s"}, ref, nil); e == nil {
				t.Fatal("cross-owner relation store accepted")
			}
			return parent.Transaction(ctx, func(child *assertions.RevisionTxn) error {
				if _, e := relations.AdmitInAssertionScope(ctx, held, child.Store, policy, relations.Context{SceneID: "s"}, ref, nil); e == nil {
					t.Fatal("ancestor relation store rebound")
				}
				return nil
			})
		})
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		if _, e := relations.AdmitInAssertionScope(ctx, held, tx.Store, policy, relations.Context{SceneID: "s"}, ref, nil); e == nil {
			t.Fatal("expired relation store rebound")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestOuterProposalCorroborationCheckpointAndRelationTombstone(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			s, f, opts := outerSetup(t)
			ctx := context.Background()
			in, c := outerInput(t, "first")
			_ = c.Set("relations", []any{outerRelation(t)})
			first, e := s.Propose(ctx, in, c, opts)
			requireOuterSaved(t, first, e)
			outerRef(t, f, first)
			head, e := f.recovery.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if fail {
				if _, e = f.main.Exec(`CREATE TRIGGER fail_replacement_relation BEFORE INSERT ON memory_relation_candidates BEGIN SELECT RAISE(ABORT,'outer_relation_tail'); END`); e != nil {
					t.Fatal(e)
				}
			}
			in, c = outerInput(t, "second")
			_ = c.Set("content", "Synthetic first")
			_ = c.Set("relations", []any{outerRelation(t)})
			second, e := s.Propose(ctx, in, c, opts)
			if fail {
				if e == nil || !strings.Contains(e.Error(), "outer_relation_tail") {
					t.Fatalf("replacement failure %v", e)
				}
				if outerCount(t, f, "SELECT count(*) FROM memory_assertion_state WHERE assertion_id=? AND version=1 AND deletion_epoch=0", text(first.Memory, "id")) != 1 {
					t.Fatal("failed outer checkpoint leaked revision")
				}
				var raw string
				if e = f.main.QueryRow("SELECT value FROM memory_relation_candidates").Scan(&raw); e != nil {
					t.Fatal(e)
				}
				if text(parseObject(t, raw), "id") != text(first.Relations.Created[0], "id") {
					t.Fatal("failed replacement lost original relation")
				}
				after, e := f.recovery.Head(ctx)
				if e != nil {
					t.Fatal(e)
				}
				gate, e := recovery.ReadGate(ctx, f.main)
				if e != nil {
					t.Fatal(e)
				}
				if after != head || gate.Head != head {
					t.Fatalf("source-local relation purge unexpectedly journaled or ACKed: before=%+v after=%+v gate=%+v", head, after, gate.Head)
				}
				return
			}
			requireOuterSaved(t, second, e)
			if text(second.Memory, "id") != text(first.Memory, "id") || len(second.Relations.Created) != 1 {
				t.Fatal("corroboration identity or relation lost")
			}
			exact := outerRef(t, f, second)
			if value(exact, "version") != 2.0 || !deepEqual(value(second.Relations.Created[0], "backing"), exact) {
				t.Fatal("relation used stale or pre-checkpoint reference")
			}
			if outerCount(t, f, "SELECT count(*) FROM memory_assertion_revisions WHERE assertion_id=?", text(second.Memory, "id")) != 2 {
				t.Fatal("double-flushed checkpoint history")
			}
			after, e := f.recovery.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			gate, e := recovery.ReadGate(ctx, f.main)
			if e != nil {
				t.Fatal(e)
			}
			if after != head || gate.Head != head {
				t.Fatalf("source-local relation purge unexpectedly changed journal: before=%+v after=%+v gate=%+v", head, after, gate.Head)
			}
		})
	}
}

func TestOuterKnowledgeDoesNotAddCommitmentPolicyReads(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			// The real assertion checkpoint legitimately resolves workspace snapshots,
			// including commitment policy. Graph admission must add no extra read.
			base, _, baseOptions := outerSetup(t)
			baselineCalls := 0
			base.d.CommitmentsEnabled = func(commitments.Context) (bool, error) { baselineCalls++; return false, nil }
			baseIn, baseCandidate := outerInput(t, "m")
			baseline, baseErr := base.Propose(context.Background(), baseIn, baseCandidate, baseOptions)
			requireOuterSaved(t, baseline, baseErr)
			s, _, opts := outerSetup(t)
			opts.KnowledgeEnabled = enabled
			calls := 0
			s.d.CommitmentsEnabled = func(commitments.Context) (bool, error) {
				calls++
				if calls > baselineCalls {
					return false, errors.New("unexpected_extra_commitment_policy")
				}
				return false, nil
			}
			in, c := outerInput(t, "m")
			_ = c.Set("knowledge", []any{outerKnowledge(t, "m")})
			out, e := s.Propose(context.Background(), in, c, opts)
			requireOuterSaved(t, out, e)
			if calls != baselineCalls {
				t.Fatalf("knowledge added policy reads: got %d baseline %d", calls, baselineCalls)
			}
			if enabled && len(out.Knowledge.Created) != 1 || !enabled && (len(out.Knowledge.Rejected) != 1 || out.Knowledge.Rejected[0].Reason != "knowledge_disabled") {
				t.Fatalf("knowledge result %+v", out.Knowledge)
			}
		})
	}
}
