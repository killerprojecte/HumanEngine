package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func sceneStateRoute(r *http.Request, path string) (string, bool) {
	if r.Method != "GET" && r.Method != "HEAD" {
		return "", false
	}
	tail, ok := strings.CutPrefix(path, "/api/scenes/")
	if !ok {
		return "", false
	}
	segment, ok := strings.CutSuffix(tail, "/state")
	if !ok || segment == "" || strings.Contains(segment, "/") {
		return "", false
	}
	return habitTryDecode(segment, false), true
}

func (s *Server) serveSceneState(w http.ResponseWriter, r *http.Request, available bool, id, sceneID string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	fail := func() {
		_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "scene_state.failed", Message: "Scene state read failed", RequestID: id})
		if !response.accepted && r.Context().Err() == nil {
			if r.Method == "HEAD" {
				w.WriteHeader(503)
			} else {
				habitError(w, 503, "Request failed; inspect server diagnostics", id)
			}
		}
	}
	if !available {
		fail()
		return
	}
	ctx := r.Context()
	owner := s.runtimeStatus
	sqlID, err := jsjson.UTF8Text(sceneID)
	if err != nil {
		fail()
		return
	}
	var exists bool
	var observed recovery.Gate
	err = owner.publisher.Publish(ctx, func(q *sql.Conn) ([]byte, error) {
		var err error
		if observed, err = recovery.ReadGate(ctx, q); err != nil {
			return nil, err
		}
		return nil, q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scenes WHERE id=?)", sqlID).Scan(&exists)
	}, func([]byte) error { return nil })
	if err != nil {
		fail()
		return
	}
	body := []byte(`{"error":"Scene unavailable"}`)
	if exists {
		body, err = json.Marshal(owner.runtime.SceneState(sceneID))
		if err != nil {
			fail()
			return
		}
	} else {
		response.status = 400
	}
	err = owner.publisher.Publish(ctx, func(q *sql.Conn) ([]byte, error) {
		current, err := recovery.ReadGate(ctx, q)
		if err != nil {
			return nil, err
		}
		if current != observed {
			return nil, errors.New("response_materialization_stale_or_unbound")
		}
		return body, nil
	}, func(data []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return response.enqueue(ctx, data)
	})
	if response.accepted {
		err = response.drain(ctx, w)
	}
	if err != nil {
		fail()
	}
}
