// Package knowledgeadmission composes concrete graph and source authority inside
// an existing assertion transaction. It is not the outer MemoryService.Propose.
package knowledgeadmission

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"time"
)

type Options struct {
	knowledge.ReadOptions
	AutoAdmission bool
	ID            func() string
}

// AdmitInAssertionScope uses the SAME active assertion child for sources,
// current backing authority and writes. Flags are the settings captured for
// this invocation; queued callers must revalidate them at execution time.
// trustedContext must be the original context, not the marker-rewritten copy.
func AdmitInAssertionScope(ctx context.Context, storage *knowledge.Store, active *assertions.Store, commitmentsStore *commitments.Store, options Options, trustedContext, backing *knowledge.Object, inputs []any) (out knowledge.AdmissionResult, err error) {
	out = knowledge.AdmissionResult{Created: []*knowledge.Object{}, Rejected: []knowledge.AdmissionRejection{}}
	if active == nil || storage == nil || commitmentsStore == nil {
		return out, errors.New("knowledge_admission_owner_required")
	}
	if options.Authority.NowMillis == nil {
		options.Authority.NowMillis = func() int64 { return time.Now().UnixMilli() }
	}
	err = active.WithWriteSession(func(session *recovery.WriteSession) error {
		graph, e := storage.BindAdmissionSession(session)
		if e != nil {
			return e
		}
		disabled := func() error {
			out.Rejected = []knowledge.AdmissionRejection{{Index: -1, Reason: "knowledge_disabled"}}
			return nil
		}
		simulation, _ := trustedContext.Get("simulation")
		if simulation == true || !options.KnowledgeEnabled {
			return disabled()
		}
		return active.Read(ctx, func(ar *assertions.Reader) error {
			sceneID, _ := trustedContext.Get("sceneId")
			scene, e := ar.Scene(asString(sceneID))
			if e != nil {
				return e
			}
			learning := false
			if scene != nil {
				v, _ := scene.Get("learningEnabled")
				learning = v == true
			}
			if !learning {
				return disabled()
			}
			source, e := sourceadmission.New(ar, options.Authority)
			if e != nil {
				return e
			}
			return graph.ReadAssertionCurrent(ctx, active, commitmentsStore, options.ReadOptions, func(reader *knowledge.Reader) error {
				var e error
				out, e = reader.Admit(graph, source, trustedContext, backing, inputs, knowledge.AdmissionPolicy{LearningEnabled: true, AutoAdmission: options.AutoAdmission, ID: options.ID})
				return e
			})
		})
	})
	return
}
func asString(v any) string { s, _ := v.(string); return s }
