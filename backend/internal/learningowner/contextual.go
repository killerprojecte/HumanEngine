package learningowner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionproposal"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/contextuallearning"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningbatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// NewContextual binds actual contextual dispatch to this learning owner's native
// dependencies. It does not install the foreground action router or fabricate
// relation/commitment/knowledge exposure capabilities. The application owns the
// returned lifetime and must CloseAndWait before closing this owner or its DB.
func (o *Owner) NewContextual(compare func(string, string) (int, error)) (*contextuallearning.Proposals, error) {
	parser, e := learningbatch.NewCandidateParser()
	if e != nil {
		return nil, e
	}
	return contextuallearning.NewProposals(contextuallearning.Ports{
		State: func(ctx context.Context, scene string, ws *recovery.WriteSession) (contextuallearning.Settings, error) {
			s, e := o.contextualSettings(ctx, ws)
			if e != nil {
				return contextuallearning.Settings{}, e
			}
			st := o.d.Assertions
			if ws != nil {
				st = st.WithSession(ws)
			}
			enabled := false
			e = st.Read(ctx, func(r *assertions.Reader) error {
				v, e := r.Scene(scene)
				enabled = get(v, "learningEnabled") == true
				return e
			})
			raw, e2 := json.Marshal(s)
			return contextuallearning.Settings{LearningEnabled: enabled, GraphEnabled: s["memoryGraphEnabled"] == true, GraphAnswerEnabled: s["memoryGraphAnswerEnabled"] == true, CommitmentsEnabled: s["memoryCommitmentsEnabled"] == true, KnowledgeEnabled: s["memoryKnowledgeEnabled"] == true, Revision: fmt.Sprintf("%x", sha256.Sum256(raw))}, errors.Join(e, e2)
		},
		Preview: func(scene string) ([]*Object, error) { s, e := o.d.Foreground.Snapshot(scene); return s.Messages, e }, ForegroundBusy: o.ForegroundBusy, Compare: compare,
		Registered: func(ctx context.Context, scene, id string) (registered bool, err error) {
			err = o.d.Sources.Read(ctx, func(v *sourceevents.View) error { ref, e := v.Registered(scene, id); registered = ref != nil; return e })
			return
		},
		CurrentSource: func(ctx context.Context, m *Object, ws *recovery.WriteSession) (bool, error) {
			s := o.d.Sources
			if ws != nil {
				s = s.WithSession(ws)
			}
			return s.CurrentRuntimeSnapshot(ctx, m)
		},
		Recalled: func(ctx context.Context, scene, id string, ws *recovery.WriteSession) (recalled bool, err error) {
			s := o.d.Assertions
			if ws != nil {
				s = s.WithSession(ws)
			}
			err = s.Read(ctx, func(r *assertions.Reader) error { var e error; recalled, e = r.Recalled(scene, id); return e })
			return
		},
		MemoryStates: func(ctx context.Context, scene string, ids []string, ws *recovery.WriteSession) (map[string]contextuallearning.MemoryState, error) {
			values, e := o.d.Memory.ContextualStates(ctx, scene, ids, ws)
			out := map[string]contextuallearning.MemoryState{}
			for id, s := range values {
				out[id] = contextuallearning.MemoryState{Version: s.Version, DeletionEpoch: s.DeletionEpoch, Deleted: s.Deleted, Visible: s.Visible}
			}
			return out, e
		},
		Commit: func(ctx context.Context, r contextuallearning.CommitRequest) (memoryservice.ProposalResult, error) {
			return o.commitContextual(ctx, r, parser)
		},
		OnEvent: func(e contextuallearning.Event) {
			level := "info"
			if e.Status == "failed" {
				level = "warn"
			}
			status := "completed"
			switch e.Status {
			case "running":
				status = "started"
			case "failed":
				status = "failed"
			case "cancelled":
				status = "cancelled"
			case "discarded":
				status = "skipped"
			}
			fields := map[string]any{"batchId": e.JobID, "stage": "learning.contextual", "status": status, "reason": "contextual_learning_" + e.Status}
			if e.Committed {
				fields["counts"] = map[string]any{"committedWrites": 1}
			}
			_ = o.d.Logs.Emit(observability.Event{Level: level, Module: "learning", Code: "contextual_learning_" + e.Status, Message: "contextual_learning_" + e.Status, Stage: "learning.contextual", Fields: fields})
		},
	})
}
func (o *Owner) contextualSettings(ctx context.Context, ws *recovery.WriteSession) (map[string]any, error) {
	if ws == nil {
		return o.settings(ctx)
	}
	rows, e := ws.Query("SELECT value FROM config WHERE key='settings'")
	if e != nil {
		return nil, e
	}
	raw := string(contract.DefaultSettingsInput())
	if rows.Next() {
		e = rows.Scan(&raw)
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return nil, e
	}
	schemas, e := contract.SharedSchemas()
	if e != nil {
		return nil, e
	}
	return schemas.Parse("Settings", []byte(raw))
}

type contextualRejection struct{ reason string }

func (e contextualRejection) Error() string { return e.reason }
func (o *Owner) commitContextual(ctx context.Context, r contextuallearning.CommitRequest, parser *learningbatch.CandidateParser) (memoryservice.ProposalResult, error) {
	var candidate *Object
	result, e := o.d.Memory.ProposeGuarded(ctx, func(ws *recovery.WriteSession) (*Object, *Object, memoryservice.ProposalOptions, error) {
		fail := func(reason string) (*Object, *Object, memoryservice.ProposalOptions, error) {
			return nil, nil, memoryservice.ProposalOptions{}, contextualRejection{reason}
		}
		if o.closed.Load() || ctx.Err() != nil {
			return fail("request_cancelled")
		}
		if r.Validate == nil {
			return fail("context_ineligible")
		}
		ok, e := r.Validate(ctx, ws)
		if e != nil {
			return nil, nil, memoryservice.ProposalOptions{}, e
		}
		if !ok {
			return fail("context_ineligible")
		}
		settings, e := o.contextualSettings(ctx, ws)
		if e != nil {
			return nil, nil, memoryservice.ProposalOptions{}, e
		}
		s := learningbatch.Settings{GraphEnabled: settings["memoryGraphEnabled"] == true, CommitmentsEnabled: settings["memoryCommitmentsEnabled"] == true, KnowledgeEnabled: settings["memoryKnowledgeEnabled"] == true}
		parsed, e := parser.ParseContextual(r.Candidate, s)
		if e != nil {
			return nil, nil, memoryservice.ProposalOptions{}, e
		}
		if (get(parsed, "subject") != nil || get(parsed, "commitment") != nil) && get(parsed, "publicTechnicalDomain") != nil {
			return fail("personal_subject_not_public_knowledge")
		}
		evidence, e := sourceadmission.TrustedEvidence(r.Evidence, func(scene, id string) (recalled bool, err error) {
			err = o.d.Assertions.WithSession(ws).Read(ctx, func(v *assertions.Reader) error { var e error; recalled, e = v.Recalled(scene, id); return e })
			return
		})
		if e != nil {
			return nil, nil, memoryservice.ProposalOptions{}, e
		}
		input := obj("sceneId", r.SceneID, "simulation", false, "provenance", "contextual", "botUserId", "bot", "sourceEvidence", evidence)
		ids := []any{}
		for _, ref := range r.Parents {
			ids = append(ids, ref.ID)
		}
		_ = input.Set("dependencyMemoryIds", ids)
		for _, field := range []struct {
			name string
			v    any
		}{{"dependencyParentReferences", r.Parents}, {"exposedEntities", r.Entities}, {"exposedCommitments", r.Commitments}, {"exposedKnowledgeEntities", r.KnowledgeEntities}, {"exposedKnowledgeEdges", r.KnowledgeEdges}} {
			v, e := contextuallearning.SnapshotJSON(field.v)
			if e != nil {
				return nil, nil, memoryservice.ProposalOptions{}, e
			}
			_ = input.Set(field.name, v)
		}
		sources := []any{}
		for _, id := range arr(get(parsed, "sourceMessageIds")) {
			sender := ""
			for _, item := range evidence {
				m, _ := item.(*Object)
				if str(m, "sceneId") == r.SceneID && get(m, "messageId") == id {
					sender = str(m, "senderId")
					break
				}
			}
			sources = append(sources, obj("sceneId", r.SceneID, "messageId", id, "senderId", sender))
		}
		candidate = parsed.Clone()
		_ = candidate.Set("sources", sources)
		attribution := sourceadmission.ResolveSubject(input, candidate)
		if get(attribution, "ok") != true {
			return fail(str(attribution, "reason"))
		}
		for _, k := range []string{"personId", "kind", "epistemicKind"} {
			if v, ok := attribution.Get(k); ok {
				_ = candidate.Set(k, v)
			} else {
				candidate.Delete(k)
			}
		}
		options := o.d.Proposal
		options.GraphEnabled = s.GraphEnabled
		options.KnowledgeEnabled = s.KnowledgeEnabled
		options.GraphAutoAdmission = settings["memoryGraphAutoAdmissionEnabled"] == true
		options.KnowledgeAutoAdmission = settings["memoryKnowledgeAutoAdmissionEnabled"] == true
		return input, candidate, options, nil
	})
	var rejected contextualRejection
	if errors.As(e, &rejected) {
		return memoryservice.ProposalResult{Result: assertionproposal.Result{Status: "rejected", Reason: rejected.reason}}, nil
	}
	if e != nil {
		return memoryservice.ProposalResult{}, e
	}
	if result.Status == "saved" {
		memory, e := o.AfterSaved(ctx, r.SceneID, candidate, result.Memory, r.Evidence)
		if e != nil {
			return memoryservice.ProposalResult{}, e
		}
		result.Memory = memory
	}
	return result, nil
}
