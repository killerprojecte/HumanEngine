package memoryservice

import (
	"context"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type KnowledgeAdminRead func(context.Context, *knowledge.AdminQuery, func(int, []byte) error) (bool, error)

func (s *Service) BindKnowledgeAdminRead(publisher *recovery.ReadPublisher, schemas *contract.Schemas) (KnowledgeAdminRead, error) {
	publish, err := s.d.Knowledge.Storage.BindAdminRead(publisher, s.d.Assertions, s.d.Commitments, s.d.Authority)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, q *knowledge.AdminQuery, emit func(int, []byte) error) (bool, error) {
		started, status := false, http.StatusOK
		response := func(code int, key string) ([]byte, error) {
			status = code
			return jsjson.Marshal(personObject("error", key))
		}
		err := publish(ctx, func(r *assertions.Reader, reader func(bool) *knowledge.Reader) ([]byte, error) {
			started = true
			if q == nil {
				return response(http.StatusBadRequest, "Invalid request")
			}
			real, err := realPeopleScene(r, q.SceneID)
			if err != nil {
				return nil, err
			}
			if !real {
				return response(http.StatusNotFound, "knowledge_scene_unavailable")
			}
			raw, exists, err := r.SettingsInput()
			if err != nil {
				return nil, err
			}
			input := []byte(raw)
			if !exists {
				input = contract.DefaultSettingsInput()
			}
			settings, err := schemas.Parse("Settings", input)
			if err != nil {
				return nil, err
			}
			enabled := settings["memoryKnowledgeEnabled"] == true
			result, err := reader(enabled).AdminQuery(q, enabled)
			var anchor knowledge.AdminAnchorError
			switch {
			case errors.As(err, &anchor):
				if anchor == knowledge.AdminAnchorUnavailable {
					return response(http.StatusNotFound, string(anchor))
				}
				return response(http.StatusBadRequest, string(anchor))
			case err != nil:
				return response(http.StatusConflict, "knowledge_query_unavailable")
			}
			if err := schemas.ValidateKnowledgeAdministrationView(result); err != nil {
				return response(http.StatusConflict, "knowledge_query_unavailable")
			}
			return jsjson.Marshal(result)
		}, func(body []byte) error { return emit(status, body) })
		return started, err
	}, nil
}
