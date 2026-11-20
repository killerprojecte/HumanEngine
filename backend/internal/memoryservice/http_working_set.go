package memoryservice

import (
	"context"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryworkspace"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

type WorkingSetQuery struct {
	EpisodesQuery
	IncludeCold bool
}

type WorkingSetAdminRead func(context.Context, *WorkingSetQuery, func(int, []byte) error) (bool, error)

var errWorkingSetChanged = errors.New("memory_working_set_changed")

const workingSetFeature = "memoryWorkingSetEnabled"

func (s *Service) BindWorkingSetAdminRead(publisher *recovery.ReadPublisher, schemas *contract.Schemas) (WorkingSetAdminRead, error) {
	publish, err := s.d.WorkingSet.BindPublishedRead(publisher)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, q *WorkingSetQuery, emit func(int, []byte) error) (bool, error) {
		var initial episodesFlags
		var candidates workspaceCandidates
		if q != nil {
			if err := s.d.Assertions.Read(ctx, func(r *assertions.Reader) (err error) {
				initial, err = loadEpisodesFlags(r, schemas, q.SceneID, workingSetFeature)
				return err
			}); err != nil {
				return false, err
			}
			if initial.real && initial.enabled {
				open := "open"
				var err error
				candidates, err = s.prepareWorkspaceCandidates(ctx, q.EpisodesQuery, false, initial.commitments, &open)
				if err != nil {
					return true, err
				}
				if err := s.refreshWorkspaceMetadata(ctx, q.SceneID, candidates.selections); err != nil {
					return true, err
				}
			}
		}
		started := false
		status := http.StatusOK
		err := publish(ctx, func(ws *workingset.Service) (body []byte, err error) {
			started = true
			if q == nil {
				status = http.StatusBadRequest
				return []byte(`{"error":"Invalid request"}`), nil
			}
			err = ws.ReadScope(ctx, func(scope *workingset.Scope) error {
				ar, err := s.workspaceAssertionReader(scope)
				if err != nil {
					return err
				}
				flags, err := loadEpisodesFlags(ar, schemas, q.SceneID, workingSetFeature)
				if err != nil {
					return err
				}
				if !flags.real {
					status = http.StatusNotFound
					body = []byte(`{"error":"Scene unavailable"}`)
					return nil
				}
				c := workingset.Context{SceneID: q.SceneID}
				pins, err := ws.ListPinPolicies(ctx, c, 200)
				if err != nil {
					return err
				}
				if !flags.enabled {
					body, err = jsjson.Marshal(workingSetBody(pins, false, "disabled", 0, []any{}))
					return err
				}
				if !initial.real || !initial.enabled || initial.commitments != flags.commitments {
					return errWorkingSetChanged
				}
				access, err := ws.View(ctx, c, candidates.selections, q.IncludeCold)
				if err != nil {
					return err
				}
				if !access.Enabled {
					return errWorkingSetChanged
				}
				rows := workingSetRows(candidates, access.Items)
				coverage := "complete"
				if candidates.assertionsCoverage == "limited" || candidates.plansCoverage == "limited" || access.Coverage == "limited" || len(rows) > 12 {
					coverage = "limited"
				}
				items, err := s.projectWorkingSetRows(ctx, scope, q.SceneID, rows[:min(len(rows), 12)])
				if err != nil {
					return err
				}
				out := workingSetBody(pins, true, coverage, access.OmittedCold, items)
				_ = out.Set("omittedEntries", float64(max(len(rows)-12, 0)))
				body, err = jsjson.Marshal(out)
				return err
			})
			return body, err
		}, func(body []byte) error { return emit(status, body) })
		return started, err
	}, nil
}

func (s *Service) workspaceAssertionReader(scope *workingset.Scope) (*assertions.Reader, error) {
	return s.d.Assertions.BorrowReader(scope.CheckScope, func(q string, args ...any) (assertions.Rows, error) {
		return scope.Query(q, args...)
	}, func(q string) (assertions.PreparedRead, error) {
		p, err := scope.PrepareQuery(q)
		return workspaceStatement{p}, err
	})
}

type workspaceStatement struct{ workingset.PreparedRead }

func (s workspaceStatement) Query(args ...any) (assertions.Rows, error) {
	return s.PreparedRead.Query(args...)
}

type workingSetRow struct {
	row    *Object
	access workingset.ViewItem
}

func workingSetRows(c workspaceCandidates, items []workingset.ViewItem) []workingSetRow {
	byEntity := make(map[workingset.Entity]workingset.ViewItem, len(items))
	for _, item := range items {
		byEntity[workingset.Entity{Kind: item.Ref.Kind, ID: item.Ref.ID}] = item
	}
	out := make([]workingSetRow, 0, len(c.assertions)+len(c.plans))
	for _, priority := range [2]bool{true, false} {
		for _, group := range [2]struct {
			kind string
			rows []any
		}{{"assertion", c.assertions}, {"commitment", c.plans}} {
			for _, raw := range group.rows {
				row := object(raw)
				item, ok := byEntity[workingset.Entity{Kind: group.kind, ID: text(object(value(row, "ref")), "id")}]
				if ok && (item.Protection != nil || item.PinIntent) == priority {
					out = append(out, workingSetRow{row, item})
				}
			}
		}
	}
	return out
}

func (s *Service) projectWorkingSetRows(ctx context.Context, scope *workingset.Scope, sceneID string, rows []workingSetRow) (items []any, err error) {
	items = make([]any, 0, len(rows))
	if len(rows) == 0 {
		return
	}
	c := commitments.Context{SceneID: sceneID}
	err = s.d.Commitments.ReadWorkingSetCurrent(ctx, scope, s.d.Assertions, s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
		resolver, err := memoryworkspace.NewResolver(read, s.d.Authority.DateParser)
		if err != nil {
			return err
		}
		for _, row := range rows {
			ref := object(value(row.row, "ref"))
			r := commitments.Reference{ID: text(ref, "id"), Version: episodeNumber(ref, "version"), DeletionEpoch: episodeNumber(ref, "deletionEpoch")}
			a := row.access
			access := epObj("ref", workingSetRef(a.Ref), "dependencyKey", a.DependencyKey, "state", a.State, "reviewDue", a.ReviewDue,
				"pinned", a.Pinned, "pinIntent", a.PinIntent, "pinRevision", a.PinRevision, "pinStatus", a.PinStatus,
				"policyCoverage", a.PolicyCoverage, "metadataRevision", a.MetadataRevision, "contextSelectedCount", a.ContextSelectedCount,
				"lastSelectedAt", workspaceNullable(a.LastSelectedAt), "protection", workspaceNullable(a.Protection))
			if a.Ref.Kind == "assertion" {
				current, err := resolver.AssertionSnapshot(c, workingset.Reference{Kind: "assertion", ID: r.ID, Version: r.Version, DeletionEpoch: r.DeletionEpoch})
				if err != nil {
					return err
				}
				if current == nil || text(current, "qualificationKey") != text(row.row, "qualificationKey") {
					return errWorkingSetChanged
				}
				record, err := projectWorkspaceRecord(object(value(current, "record")))
				if err != nil {
					return err
				}
				items = append(items, epObj("kind", "assertion", "record", record, "accessibility", access))
				continue
			}
			// The source joins accessibility by entity; one commitment can emit
			// active and latest revisions. Each row still proves its exact revision.
			current, err := read.Current(c, r)
			if err != nil {
				return err
			}
			if current == nil || !deepEqual(current, row.row) {
				return errWorkingSetChanged
			}
			items = append(items, epObj("kind", "commitment", "view", projectWorkspacePlan(current), "accessibility", access))
		}
		return nil
	})
	return
}

func workingSetBody(pins workingset.PinList, enabled bool, coverage string, omittedCold int, items []any) *Object {
	policies := make([]any, 0, len(pins.Items))
	for _, p := range pins.Items {
		policies = append(policies, epObj("schemaVersion", float64(p.SchemaVersion), "sceneId", p.SceneID,
			"selection", epObj("ref", workingSetRef(p.Selection.Ref), "dependencyKey", p.Selection.DependencyKey),
			"policyRevision", p.PolicyRevision, "pinned", p.Pinned, "terminal", p.Terminal))
	}
	return epObj("policies", epObj("items", policies, "coverage", pins.Coverage), "enabled", enabled,
		"coverage", coverage, "omittedCold", float64(omittedCold), "items", items)
}

func workingSetRef(r workingset.Reference) *Object {
	out := epObj("kind", r.Kind, "id", r.ID, "version", r.Version, "deletionEpoch", r.DeletionEpoch)
	if r.MutationVersion != nil {
		_ = out.Set("mutationVersion", *r.MutationVersion)
	}
	return out
}

func workspaceNullable[T string | float64](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}
