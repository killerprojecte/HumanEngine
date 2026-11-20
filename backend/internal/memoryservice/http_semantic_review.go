package memoryservice

import (
	"context"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
)

type SemanticReviewQuery struct {
	ID, SceneID, Status string
	Version             float64
}

var errSemanticReviewScene = errors.New("semantic_scene_unavailable")

// ReviewSemantic runs the existing review transition once, in the installation's
// revision writer. The response is just the committed scalar acknowledgement.
func (a *AdminRead) ReviewSemantic(ctx context.Context, q *SemanticReviewQuery, parseErr error, schemas *contract.Schemas, emit func(int, []byte) error) (bool, error) {
	code := http.StatusBadRequest
	result := epObj("error", "Invalid request")
	if q != nil {
		var next *Object
		started := false
		s := a.memory
		err := s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
			var enabled bool
			if err := tx.Store.Read(ctx, func(r *assertions.Reader) error {
				real, err := realPeopleScene(r, q.SceneID)
				if err != nil {
					return err
				}
				if !real {
					return errSemanticReviewScene
				}
				started = true
				raw, exists, err := r.SettingsInput()
				if err != nil {
					return err
				}
				input := contract.DefaultSettingsInput()
				if exists {
					input = []byte(raw)
				}
				settings, err := schemas.Parse("Settings", input)
				enabled = settings["memorySemanticEnabled"] == true
				return err
			}); err != nil {
				return err
			}
			return tx.Store.WithWriteSession(func(ws *recovery.WriteSession) (err error) {
				next, err = s.d.Semantic.WithSession(ws).Review(semantic.Context{SceneID: q.SceneID},
					q.ID, q.Version, q.Status, enabled, s.semanticPrivacyAuthority(ctx, tx, ws))
				return err
			})
		})
		switch {
		case err == nil:
			code = http.StatusOK
			result = semanticAdminFields(next, "id", "version", "status")
		case errors.Is(err, errSemanticReviewScene):
			code, result = http.StatusNotFound, epObj("error", errSemanticReviewScene.Error())
		case started:
			code, result = http.StatusConflict, epObj("error", "semantic_review_conflict_or_unavailable")
		default:
			return false, err
		}
	}
	// The publisher refuses all output if a failed transition left a loose
	// durable intent, including an otherwise ordinary-looking 409.
	return a.publishBody(ctx, func(*assertions.Reader) ([]byte, error) {
		if parseErr != nil {
			return nil, parseErr
		}
		return jsjson.Marshal(result)
	}, func(body []byte) error { return emit(code, body) })
}
