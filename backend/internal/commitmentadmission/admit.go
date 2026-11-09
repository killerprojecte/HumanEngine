// Package commitmentadmission composes command admission with actual assertion,
// journal and working-set owners. It is not outer ProposalCommand or a robot API.
package commitmentadmission

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryworkspace"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

type Object = jsjson.Object
type Options struct {
	Authority      assertionauthority.Options
	Enabled        bool
	ID             func() (string, error)
	Dirty          func()
	DirtyInSession func(*recovery.WriteSession)
}

func get(v any, k string) any {
	o, _ := v.(*Object)
	if o == nil {
		return nil
	}
	x, _ := o.Get(k)
	return x
}
func str(v any) string  { s, _ := v.(string); return s }
func num(v any) float64 { n, _ := v.(float64); return n }
func arr(v any) []any   { a, _ := v.([]any); return a }

// AdmitInAssertionScope uses the original trusted context and exact checkpointed
// backing. The caller owns rollback; an error must escape its assertion child.
func AdmitInAssertionScope(ctx context.Context, plans *commitments.Store, active *assertions.Store, workspace *workingset.Service, options Options, trustedContext *Object, backing, command any) (out commitments.CommandResult, err error) {
	if plans == nil || active == nil || workspace == nil {
		return out, errors.New("commitment_admission_owner_required")
	}
	err = active.WithWriteSession(func(session *recovery.WriteSession) error {
		if workspace.Session() != nil && workspace.Session() != session {
			return errors.New("commitment_workspace_session_binding")
		}
		bound, e := plans.BindCommandSession(session)
		if e != nil {
			return e
		}
		if get(trustedContext, "simulation") == true || !options.Enabled {
			out = commitments.CommandResult{Status: "rejected", Reason: "commitment_learning_disabled"}
			return nil
		}
		return active.Read(ctx, func(ar *assertions.Reader) error {
			scene, e := ar.Scene(str(get(trustedContext, "sceneId")))
			if e != nil {
				return e
			}
			if get(scene, "learningEnabled") != true {
				out = commitments.CommandResult{Status: "rejected", Reason: "commitment_learning_disabled"}
				return nil
			}
			enabled := func(commitments.Context) (bool, error) { return options.Enabled, nil }
			owners := commitments.Owners{AfterWrite: func(store *commitments.Store, head *Object, next, prior []any) error {
				return store.WithWriteSession(func(owner *recovery.WriteSession) error {
					if owner != session {
						return errors.New("commitment_store_session_binding")
					}
					ws := workspace.WithSession(owner)
					resolve := func(child *recovery.WriteSession, c workingset.Context, refs []workingset.Reference) (result []workingset.Snapshot, err error) {
						err = plans.ReadActiveAssertionCurrent(ctx, child, active.WithSession(child), enabled, options.Authority, func(read *commitments.ReadService) error {
							resolver, e := memoryworkspace.NewResolver(read, options.Authority.DateParser)
							if e != nil {
								return e
							}
							result, e = resolver.Resolve(c, refs)
							return e
						})
						return
					}
					mutations := ws.Mutations(workingset.MutationPorts{ResolveCommitted: resolve})
					scene := str(get(head, "sceneId"))
					entity := []workingset.Entity{{Kind: "commitment", ID: str(get(head, "id"))}}
					if get(head, "deleted") == true {
						if e := ws.TerminalInvalidate(ctx, scene, entity); e != nil {
							return e
						}
					} else {
						if e := ws.Invalidate(ctx, scene, entity); e != nil {
							return e
						}
					}
					c := workingset.Context{SceneID: scene}
					if get(head, "deleted") != true {
						first := get(head, "activeVersion")
						if first == nil {
							first = get(head, "latestVersion")
						}
						versions := []any{first}
						if first != get(head, "latestVersion") {
							versions = append(versions, get(head, "latestVersion"))
						}
						mutation := num(get(head, "mutationVersion"))
						for _, v := range versions {
							if _, e := mutations.Committed(c, []workingset.Reference{{Kind: "commitment", ID: str(get(head, "id")), Version: num(v), DeletionEpoch: num(get(head, "deletionEpoch")), MutationVersion: &mutation}}); e != nil {
								return e
							}
						}
					}
					// Source Map preserves first insertion order and last value by assertion ID.
					refs := []workingset.Reference{}
					indices := map[string]int{}
					for _, revisions := range [][]any{prior, next} {
						for _, revision := range revisions {
							evidence := append([]any{}, arr(get(revision, "evidence"))...)
							for _, group := range arr(get(revision, "requiredProposalGroups")) {
								evidence = append(evidence, arr(group)...)
							}
							for _, ev := range evidence {
								ref := get(ev, "qualificationRef")
								if ref == nil {
									ref = get(ev, "backing")
								}
								r := workingset.Reference{Kind: "assertion", ID: str(get(ref, "id")), Version: num(get(ref, "version")), DeletionEpoch: num(get(ref, "deletionEpoch"))}
								if i, ok := indices[r.ID]; ok {
									refs[i] = r
								} else {
									indices[r.ID] = len(refs)
									refs = append(refs, r)
								}
							}
						}
					}
					for i := 0; i < len(refs); i += 40 {
						end := i + 40
						if end > len(refs) {
							end = len(refs)
						}
						if _, e := mutations.SourceEdited(c, refs[i:end]); e != nil {
							return e
						}
					}
					if options.DirtyInSession != nil {
						options.DirtyInSession(owner)
					} else if options.Dirty != nil {
						options.Dirty()
					}
					return nil
				})
			}}
			return bound.WithCommands(ctx, active, enabled, options.Authority, owners, func(service *commitments.CommandService) error {
				var e error
				out, e = service.Admit(trustedContext, backing, command, commitments.CommandPolicy{LearningEnabled: true, ID: options.ID})
				return e
			})
		})
	})
	return
}
