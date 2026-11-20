package memoryservice

import (
	"context"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/relations"
)

type RelationReviewQuery struct {
	ID, SceneID, Status string
	ExpectedVersion     float64
}

var errReviewScene = errors.New("relation_review_scene_unavailable")

// ReviewAndPublishRelation commits the derived row before refreshing support.
// Each phase has its existing owner; an error after commit never runs review again.
func (a *AdminRead) ReviewAndPublishRelation(ctx context.Context, q *RelationReviewQuery, schemas *contract.Schemas, emit func(int, []byte) error) (bool, error) {
	status := http.StatusOK
	var body []byte
	set := func(code int, errorJSON string) {
		status, body = code, []byte(errorJSON)
	}
	sceneUnavailable := func() { set(http.StatusNotFound, `{"error":"Scene unavailable"}`) }
	relationUnavailable := func() { set(http.StatusNotFound, `{"error":"Relation unavailable"}`) }
	conflict := func() { set(http.StatusConflict, `{"error":"Relation changed; refresh before reviewing"}`) }
	var expected float64
	if q == nil {
		set(http.StatusBadRequest, `{"error":"Invalid request"}`)
	} else if q.SceneID == "" {
		sceneUnavailable()
	} else {
		var err error
		expected, err = a.reviewRelation(ctx, *q, schemas)
		switch {
		case errors.Is(err, errReviewScene):
			sceneUnavailable()
		case errors.Is(err, relations.ErrUnavailable):
			relationUnavailable()
		case errors.Is(err, relations.ErrVersionConflict):
			conflict()
		case err != nil:
			return true, err
		default:
			if err := a.memory.RefreshSupport(ctx); err != nil {
				return true, err
			}
		}
	}
	return a.publishBody(ctx, func(r *assertions.Reader) ([]byte, error) {
		if body != nil {
			return body, nil
		}
		real, err := realPeopleScene(r, q.SceneID)
		if err != nil {
			return nil, err
		}
		if !real {
			sceneUnavailable()
			return body, nil
		}
		options, _, err := a.relationAdminOptions(r, schemas)
		if err != nil {
			return nil, err
		}
		row, err := relations.CurrentInReadScope(r, options, relations.Context{SceneID: q.SceneID, Provenance: "contextual"}, q.ID)
		if err != nil {
			return nil, err
		}
		if row == nil {
			relationUnavailable()
			return body, nil
		}
		if value(row, "version") != expected {
			conflict()
			return body, nil
		}
		items, err := a.relationAdminItems(r, q.SceneID, []*Object{row}, schemas)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			relationUnavailable()
			return body, nil
		}
		return jsjson.Marshal(items[0])
	}, func(body []byte) error { return emit(status, body) })
}

func (a *AdminRead) reviewRelation(ctx context.Context, q RelationReviewQuery, schemas *contract.Schemas) (version float64, err error) {
	err = a.memory.d.Assertions.Transaction(ctx, func(active *assertions.Store) error {
		var options relations.Options
		err := active.Read(ctx, func(r *assertions.Reader) error {
			real, err := realPeopleScene(r, q.SceneID)
			if err != nil {
				return err
			}
			if !real {
				return errReviewScene
			}
			options, _, err = a.relationAdminOptions(r, schemas)
			return err
		})
		if err != nil {
			return err
		}
		row, err := relations.ReviewInAssertionScope(ctx, a.memory.d.Relations, active, options,
			relations.Context{SceneID: q.SceneID, Provenance: "contextual"}, q.ID, q.ExpectedVersion, q.Status)
		if err != nil {
			return err
		}
		if row == nil {
			return errors.New("relation_review_result_missing")
		}
		version, _ = value(row, "version").(float64)
		return nil
	})
	return
}
