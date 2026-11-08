package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryread"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"sort"
)

func (s *offlineFacade) semanticPrivacyAuthority(ctx context.Context, tx *assertions.RevisionTxn, ws *recovery.MaintenanceTx) semantic.PrivacyAuthority {
	current := func(c semantic.Context, ref *Object) (out *Object, err error) {
		err = s.d.Commitments.WithMaintenanceTx(ctx, ws).ReadMaintenanceCurrent(ctx, tx.Store, s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
			var e error
			out, e = semanticCurrent(read, Context{SceneID: c.SceneID, Simulation: c.Simulation}, ref)
			return e
		})
		return
	}
	anchor := func(c semantic.Context, ref *Object) (out *Object, err error) {
		err = s.d.Commitments.WithMaintenanceTx(ctx, ws).ReadMaintenanceCurrent(ctx, tx.Store, s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
			v, e := read.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, semanticRef(ref))
			if e != nil || v == nil {
				return e
			}
			out = epObj("record", v.Record, "proofs", v.Proofs)
			return nil
		})
		return
	}
	return semantic.PrivacyAuthority{
		AssertReady: func() error { return ready(ctx, tx.Store) }, Current: current, AnchorCurrent: anchor,
		PartitionKey: func(c semantic.Context, person any) (string, error) {
			if e := ready(ctx, tx.Store); e != nil {
				return "", e
			}
			if !c.Simulation {
				if e := tx.Transaction(ctx, s.refresh); e != nil {
					return "", e
				}
			}
			records, e := s.read(ctx, tx.Store, memoryread.Input{SceneID: c.SceneID, Simulation: c.Simulation})
			if e != nil {
				return "", e
			}
			selected := []*Object{}
			for _, r := range records {
				scope := object(value(r, "scope"))
				if !truthy(value(r, "internalUse")) && text(r, "origin") == "learned" && text(scope, "kind") == "scene" && text(scope, "sceneId") == c.SceneID && deepEqual(value(scope, "personId"), person) {
					selected = append(selected, r)
				}
			}
			if len(selected) < 2 || len(selected) > 12 {
				return "", nil
			}
			refs, e := s.capturePrivacyParents(ctx, tx, c, selected)
			if e != nil {
				return "", e
			}
			selections := []any{}
			for _, ref := range refs {
				live, e := current(c, ref)
				if e != nil {
					return "", e
				}
				if live != nil {
					selections = append(selections, epObj("ref", ref, "dependencyKey", value(live, "dependencyKey")))
				}
			}
			if len(selections) != len(selected) {
				return "", nil
			}
			sort.SliceStable(selections, func(i, j int) bool {
				return epLess(text(object(epGet(selections[i], "ref")), "id"), text(object(epGet(selections[j], "ref")), "id"))
			})
			return semanticHash(selections)
		},
		EpisodeAnchor: func(c semantic.Context, anchor *Object, parents []any, key string) (out bool, err error) {
			// Read the actual saved settings, using the same contract defaults as Store.
			raw := string(contract.DefaultSettingsInput())
			rows, e := ws.QueryContext(ctx, "SELECT value FROM config WHERE key=?", "settings")
			if e != nil {
				return false, e
			}
			if rows.Next() {
				e = rows.Scan(&raw)
			}
			closeErr := rows.Close()
			if e != nil {
				return false, e
			}
			if closeErr != nil {
				return false, closeErr
			}
			schemas, e := contract.SharedSchemas()
			if e != nil {
				return false, e
			}
			settings, e := schemas.Parse("Settings", []byte(raw))
			if e != nil {
				return false, e
			}
			opts := knowledge.ReadOptions{KnowledgeEnabled: settings["memoryKnowledgeEnabled"] == true, Authority: s.d.Authority}
			err = s.d.Knowledge.WithMaintenanceTx(ctx, ws).Storage.ReadMaintenanceEpisode(ctx, tx.Store, s.d.Commitments.WithMaintenanceTx(ctx, ws), opts, func(reader *knowledge.Reader, read *commitments.ReadService) error {
				ports := episodePorts{query: reader.Query, current: func(c Context, r *Object) (*Object, error) { return semanticCurrent(read, c, r) }, anchor: func(c Context, r *Object) (*commitments.Assertion, error) {
					return read.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, semanticRef(r))
				}}
				var e error
				out, e = ports.valid(Context{SceneID: c.SceneID, Simulation: c.Simulation}, anchor, parents, &key)
				return e
			})
			return
		},
	}
}

// This is captureParentReferences inside the caller's existing revision owner:
// nested refresh and legacy history import share its revision bookkeeping.
func (s *offlineFacade) capturePrivacyParents(ctx context.Context, tx *assertions.RevisionTxn, c semantic.Context, returned []*Object) (out []*Object, err error) {
	out = []*Object{}
	if c.Simulation {
		return out, nil
	}
	err = tx.Transaction(ctx, func(child *assertions.RevisionTxn) error {
		if e := ready(ctx, child.Store); e != nil {
			return e
		}
		if e := child.Transaction(ctx, s.refresh); e != nil {
			return e
		}
		ids := []string{}
		for _, r := range returned {
			ids = append(ids, text(r, "id"))
		}
		records, e := s.read(ctx, child.Store, memoryread.Input{SceneID: c.SceneID, IDs: ids})
		if e != nil {
			return e
		}
		current := map[string]*Object{}
		for _, r := range records {
			current[text(r, "id")] = r
		}
		return child.Store.Read(ctx, func(r *assertions.Reader) error {
			a, e := assertionauthority.New(r, s.d.Authority)
			if e != nil {
				return e
			}
			for _, record := range returned {
				body := record.Clone()
				body.Delete("score")
				id := text(record, "id")
				authorized := current[id]
				if authorized == nil || !deepEqual(authorized, body) {
					continue
				}
				state, e := r.State(id)
				if e != nil {
					return e
				}
				if state.Deleted {
					continue
				}
				if state.Version == 0 && state.DeletionEpoch == 0 {
					proofs, e := a.HistorySupports(id)
					if e != nil {
						return e
					}
					if _, e = child.Store.ImportLegacy(authorized, proofs); e != nil {
						return e
					}
					state, e = r.State(id)
					if e != nil {
						return e
					}
				}
				if state.Version > 0 {
					out = append(out, refObject(Reference{ID: id, Version: state.Version, DeletionEpoch: state.DeletionEpoch}))
				}
			}
			return nil
		})
	})
	return
}
func (s *offlineFacade) captureSemanticPrivacy(ctx context.Context, tx *assertions.RevisionTxn) (out map[string][]*Object, err error) {
	out = map[string][]*Object{}
	err = tx.Store.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
		store := s.d.Semantic.WithMaintenanceTx(ctx, ws)
		rows, e := store.Rows(nil)
		if e != nil {
			return e
		}
		for _, row := range rows {
			scene := text(row, "sceneId")
			if _, ok := out[scene]; ok {
				continue
			}
			captured, e := store.CaptureQualification(semantic.Context{SceneID: scene}, s.semanticPrivacyAuthority(ctx, tx, ws))
			if e != nil {
				return e
			}
			out[scene] = captured
		}
		return nil
	})
	return
}
