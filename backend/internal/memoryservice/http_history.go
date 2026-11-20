package memoryservice

import (
	"context"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type HistoryQuery struct{ SceneID, ID string }

// PublishHistory prepares legacy history outside the read publisher, then
// requalifies the anchor and every retained version at response publication.
func (a *AdminRead) PublishHistory(ctx context.Context, q *HistoryQuery, emit func(int, []byte) error) (bool, error) {
	status := http.StatusOK
	var allowed, traversalLimit bool
	if q != nil {
		var real, admitted bool
		err := a.memory.d.Assertions.Read(ctx, func(r *assertions.Reader) (err error) {
			admitted = true
			real, err = realPeopleScene(r, q.SceneID)
			return err
		})
		if err != nil {
			return admitted, err
		}
		if real {
			allowed, err = a.memory.prepareHistoricalAnchor(ctx, Context{SceneID: q.SceneID}, q.ID)
			traversalLimit = errors.Is(err, assertionauthority.ErrProofTraversalLimit)
			if err != nil && !traversalLimit {
				return true, err
			}
		}
	}
	return a.publishBody(ctx, func(r *assertions.Reader) ([]byte, error) {
		if q == nil {
			status = http.StatusBadRequest
			return []byte(`{"error":"Invalid request"}`), nil
		}
		if allowed || traversalLimit {
			real, err := realPeopleScene(r, q.SceneID)
			if err != nil {
				return nil, err
			}
			if real {
				if !traversalLimit {
					history, err := a.memory.historyForRead(r, Context{SceneID: q.SceneID}, q.ID)
					if err == nil && history != nil {
						return projectAdminHistory(history)
					}
					if err != nil && !errors.Is(err, assertionauthority.ErrProofTraversalLimit) {
						return nil, err
					}
					traversalLimit = err != nil
				}
				if traversalLimit {
					status = http.StatusServiceUnavailable
					return []byte(`{"error":"Memory history exceeded its safe evidence traversal limit"}`), nil
				}
			}
		}
		status = http.StatusNotFound
		return []byte(`{"error":"Memory unavailable"}`), nil
	}, func(body []byte) error { return emit(status, body) })
}

func projectAdminHistory(history *Object) ([]byte, error) {
	rows := value(history, "revisions").([]any)
	for i, row := range rows {
		revision := object(row)
		record := object(value(revision, "record"))
		binding := "none"
		for _, raw := range value(revision, "supports").([]any) {
			proof := object(raw)
			if parents, _ := value(proof, "parents").([]any); len(parents) == 0 {
				continue
			}
			if assertionauthority.ParentBindingState(proof) == "legacy_unknown" {
				binding = "legacy_unknown"
				break
			}
			binding = "bound"
		}
		view := personObject(
			"sourceBodyCoverage", "unavailable",
			"version", value(revision, "version"),
			"recordedAt", value(revision, "recordedAt"),
			"validFrom", value(revision, "validFrom"),
			"validTo", value(revision, "validTo"),
			"content", value(record, "content"),
			"status", value(record, "status"),
			"kind", value(record, "kind"),
		)
		for _, key := range []string{"epistemicKind", "occurredAt"} {
			if v := value(record, key); truthy(v) {
				_ = view.Set(key, v)
			}
		}
		_ = view.Set("origin", value(record, "origin"))
		_ = view.Set("parentBinding", binding)
		rows[i] = view
	}
	return jsjson.Marshal(personObject("revisions", rows, "coverage", value(history, "coverage")))
}
