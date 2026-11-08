package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionproposal"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitmentadmission"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledgeadmission"
	"github.com/killerprojecte/HumanEngine/backend/internal/relations"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"math"
)

// ProposalOptions are trusted runtime settings, never model candidate fields.
// Callers capture settings at execution, not when queueing a proposal. This
// service command does not itself expose an HTTP or bot compatibility facade.
type ProposalOptions struct {
	GraphEnabled, GraphAutoAdmission         bool
	KnowledgeEnabled, KnowledgeAutoAdmission bool
	Runtime                                  assertionproposal.Runtime
	RelationID, KnowledgeID                  func() string
	CommitmentID                             func() (string, error)
}
type ProposalResult struct {
	assertionproposal.Result
	Knowledge  *knowledge.AdmissionResult `json:"knowledge,omitempty"`
	Relations  *relations.Admission       `json:"relations,omitempty"`
	Commitment *commitments.CommandResult `json:"commitment,omitempty"`
}

// Propose preserves the outer transaction and the nested assertion transaction.
// A typed rejection is a saved backing with a rejected typed result. A returned
// error aborts all database writes, while the recovery owner retains any intent.
func (s *Service) Propose(ctx context.Context, input, candidate *Object, options ProposalOptions) (out ProposalResult, err error) {
	err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		var e error
		out, e = proposeOuter(input, candidate, proposalPorts{
			assertion: func(in, c *Object) (assertionproposal.Result, error) {
				return s.proposeAssertion(ctx, tx, in, c, options.Runtime)
			},
			checkpoint: func(id string) (*Object, error) {
				if e := tx.Checkpoint(); e != nil {
					return nil, e
				}
				var ref *Object
				e := tx.Store.Read(ctx, func(r *assertions.Reader) error {
					state, e := r.State(id)
					if e != nil {
						return e
					}
					ref = new(Object)
					_ = ref.Set("id", id)
					_ = ref.Set("version", float64(state.Version))
					_ = ref.Set("deletionEpoch", float64(state.DeletionEpoch))
					return nil
				})
				return ref, e
			},
			relations: func(in, ref *Object, proposals []any) (relations.Admission, error) {
				exposures := []*Object{}
				for _, v := range proposalArray(value(in, "exposedEntities")) {
					exposures = append(exposures, object(v))
				}
				policy := relations.Options{Authority: s.d.Authority, NowMillis: s.d.Authority.NowMillis, ID: options.RelationID,
					Enabled:       func(relations.Context) (bool, error) { return options.GraphEnabled, nil },
					AutoAdmission: func(relations.Context) (bool, error) { return options.GraphAutoAdmission, nil },
					LearningEnabled: func(c relations.Context) (enabled bool, err error) {
						err = tx.Store.Read(ctx, func(r *assertions.Reader) error {
							scene, e := r.Scene(c.SceneID)
							enabled = value(scene, "learningEnabled") == true
							return e
						})
						return
					},
				}
				return relations.AdmitInAssertionScope(ctx, s.d.Relations, tx.Store, policy, relations.Context{SceneID: text(in, "sceneId"), Simulation: value(in, "simulation") == true, Provenance: text(in, "provenance"), ExposedEntities: exposures}, ref, proposals)
			},
			commitment: func(in, ref *Object, command any) (commitments.CommandResult, error) {
				enabled, e := s.d.CommitmentsEnabled(commitments.Context{SceneID: text(in, "sceneId"), Simulation: value(in, "simulation") == true})
				if e != nil {
					return commitments.CommandResult{}, e
				}
				return commitmentadmission.AdmitInAssertionScope(ctx, s.d.Commitments, tx.Store, s.d.WorkingSet, commitmentadmission.Options{Authority: s.d.Authority, Enabled: enabled, ID: options.CommitmentID, Dirty: s.d.Dirty}, in, ref, command)
			},
			knowledge: func(in, ref *Object, proposals []any) (knowledge.AdmissionResult, error) {
				// Knowledge borrows only CurrentAssertion from the commitment read owner.
				// That path does not consult commitment policy; do not introduce a settings
				// dependency absent from the source, even when knowledge is enabled.
				return knowledgeadmission.AdmitInAssertionScope(ctx, s.d.Knowledge.Storage, tx.Store, s.d.Commitments, knowledgeadmission.Options{ReadOptions: knowledge.ReadOptions{KnowledgeEnabled: options.KnowledgeEnabled, Authority: s.d.Authority}, AutoAdmission: options.KnowledgeAutoAdmission, ID: options.KnowledgeID}, in, ref, proposals)
			},
		})
		return e
	})
	if err != nil {
		return ProposalResult{}, err
	}
	return
}

// Private ports isolate the source orchestration oracle. Production supplies
// only the concrete same-owner adapters above; no model or TypeScript proxy.
type proposalPorts struct {
	assertion  func(*Object, *Object) (assertionproposal.Result, error)
	checkpoint func(string) (*Object, error)
	relations  func(*Object, *Object, []any) (relations.Admission, error)
	commitment func(*Object, *Object, any) (commitments.CommandResult, error)
	knowledge  func(*Object, *Object, []any) (knowledge.AdmissionResult, error)
}

func proposalArray(v any) []any { a, _ := v.([]any); return a }
func proposalTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	default:
		return true
	}
}
func proposeOuter(input, candidate *Object, ports proposalPorts) (out ProposalResult, err error) {
	assertionContext, assertionCandidate := input, candidate
	reject := func(reason string) (ProposalResult, error) {
		return ProposalResult{Result: assertionproposal.Result{Status: "rejected", Reason: reason}}, nil
	}
	if len(proposalArray(value(candidate, "knowledge"))) > 0 {
		prepared, e := sourceadmission.PrepareKnowledge(input, candidate)
		if e != nil {
			return out, e
		}
		if !prepared.OK {
			return reject(prepared.Reason)
		}
		assertionContext, assertionCandidate = prepared.Context, prepared.Candidate
	}
	if proposalTruthy(value(candidate, "commitment")) {
		prepared, e := sourceadmission.PrepareCommitment(input, candidate)
		if e != nil {
			return out, e
		}
		if !prepared.OK {
			return reject(prepared.Reason)
		}
		assertionContext, assertionCandidate = prepared.Context, prepared.Candidate
	}
	result, e := ports.assertion(assertionContext, assertionCandidate)
	if e != nil {
		return out, e
	}
	out.Result = result
	rp, kp := proposalArray(value(candidate, "relations")), proposalArray(value(candidate, "knowledge"))
	cp := value(candidate, "commitment")
	if result.Status != "saved" || len(rp) == 0 && !proposalTruthy(cp) && len(kp) == 0 {
		return out, nil
	}
	ref, e := ports.checkpoint(text(result.Memory, "id"))
	if e != nil {
		return out, e
	}
	trusted := input.Clone()
	if value(trusted, "provenance") == nil {
		_ = trusted.Set("provenance", "source-only")
	}
	if len(rp) > 0 {
		r, e := ports.relations(trusted, ref, rp)
		if e != nil {
			return out, e
		}
		out.Relations = &r
	}
	if proposalTruthy(cp) {
		in := trusted.Clone()
		copyProposalField(in, "exposedRefs", input, "exposedCommitments")
		r, e := ports.commitment(in, ref, cp)
		if e != nil {
			return out, e
		}
		out.Commitment = &r
	}
	if len(kp) > 0 {
		in := trusted.Clone()
		copyProposalField(in, "exposedEntities", input, "exposedKnowledgeEntities")
		copyProposalField(in, "exposedEdges", input, "exposedKnowledgeEdges")
		copyProposalField(in, "exposedAssertions", input, "dependencyParentReferences")
		r, e := ports.knowledge(in, ref, kp)
		if e != nil {
			return out, e
		}
		out.Knowledge = &r
	}
	return out, nil
}

// Absent JS undefined properties disappear from the JSON-shaped context.
func copyProposalField(dst *Object, key string, src *Object, source string) {
	if v, ok := src.Get(source); ok {
		_ = dst.Set(key, v)
	} else {
		dst.Delete(key)
	}
}

// Existing domain result structs are internal. Serialize the outer wire result
// explicitly, preserving lowercase source fields and absent optional branches.
func (r ProposalResult) MarshalJSON() ([]byte, error) {
	out := new(Object)
	_ = out.Set("status", r.Status)
	if r.Reason != "" {
		_ = out.Set("reason", r.Reason)
	}
	if r.Memory != nil {
		_ = out.Set("memory", r.Memory)
	}
	if r.Knowledge != nil {
		branch := new(Object)
		created := []any{}
		for _, v := range r.Knowledge.Created {
			created = append(created, v)
		}
		rejected := []any{}
		for _, v := range r.Knowledge.Rejected {
			rejected = append(rejected, proposalRejection(v.Index, v.Reason))
		}
		_ = branch.Set("created", created)
		_ = branch.Set("rejected", rejected)
		_ = out.Set("knowledge", branch)
	}
	if r.Relations != nil {
		branch := new(Object)
		created := []any{}
		for _, v := range r.Relations.Created {
			created = append(created, v)
		}
		rejected := []any{}
		for _, v := range r.Relations.Rejected {
			rejected = append(rejected, proposalRejection(v.Index, v.Reason))
		}
		_ = branch.Set("created", created)
		_ = branch.Set("rejected", rejected)
		_ = out.Set("relations", branch)
	}
	if r.Commitment != nil {
		branch := new(Object)
		_ = branch.Set("status", r.Commitment.Status)
		if r.Commitment.Reason != "" {
			_ = branch.Set("reason", r.Commitment.Reason)
		}
		if r.Commitment.Item != nil {
			_ = branch.Set("item", r.Commitment.Item)
		}
		_ = out.Set("commitment", branch)
	}
	return jsjson.Marshal(out)
}
func proposalRejection(index int, reason string) *Object {
	out := new(Object)
	_ = out.Set("index", float64(index))
	_ = out.Set("reason", reason)
	return out
}
