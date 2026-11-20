package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func sceneReadRoute(r *http.Request, path string) (string, string) {
	if r.Method != "GET" && r.Method != "HEAD" {
		return "", ""
	}
	tail, ok := strings.CutPrefix(path, "/api/scenes/")
	if !ok {
		return "", ""
	}
	segment, operation, ok := strings.Cut(tail, "/")
	if !ok || segment == "" {
		return "", ""
	}
	switch operation {
	case "state", "messages":
	case "history":
		sources, explicit := honoQueryValues(r.URL.RawQuery)["source"]
		if !explicit || sources[0] == "remote" {
			operation = "history-remote"
		} else if sources[0] != "local" {
			operation = "history-invalid"
		}
	default:
		return "", ""
	}
	return habitTryDecode(segment, false), operation
}

func (s *Server) serveSceneRead(w http.ResponseWriter, r *http.Request, available bool, id, sceneID, operation string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	fail := func() {
		_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "scene_" + operation + ".failed", Message: "Scene read failed", RequestID: id})
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
	var kind string
	var scene botruntime.Scene
	var observed recovery.Gate
	err = owner.publisher.Publish(ctx, func(q *sql.Conn) ([]byte, error) {
		var err error
		if observed, err = recovery.ReadGate(ctx, q); err != nil {
			return nil, err
		}
		switch operation {
		case "state":
			return nil, q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scenes WHERE id=?)", sqlID).Scan(&exists)
		case "history":
			return nil, q.QueryRowContext(ctx, "SELECT COALESCE((SELECT json_extract(value,'$.kind') FROM scenes WHERE id=?),'')", sqlID).Scan(&kind)
		case "history-remote":
			scene, err = browserScene(ctx, q, sqlID)
			return nil, err
		}
		return nil, nil
	}, func([]byte) error { return nil })
	if err != nil {
		fail()
		return
	}
	body := []byte(`{"error":"Scene unavailable"}`)
	switch operation {
	case "messages":
		body, err = sceneMessagesBody(owner.runtime, sceneID, s.memes != nil)
	case "history", "history-remote":
		body = []byte(`{"items":[],"coverage":"unavailable","reason":"real_scene_required"}`)
		if operation == "history" {
			if kind != "" && kind != "simulation" {
				var rows []*botruntime.Object
				var links map[*botruntime.Object]botruntime.DisplayImageLinks
				rows, links, err = owner.runtime.LocalHistory(sceneID, s.memes != nil)
				if err == nil {
					reason := "local_history_window"
					body, err = browserHistoryBody(botruntime.BrowserHistory{Items: rows, ImageLinks: links, Coverage: "partial", Reason: &reason})
				}
			}
		} else if scene.ID == sceneID && scene.Kind != "" && scene.Kind != "simulation" {
			var result botruntime.BrowserHistory
			result, err = owner.remoteHistory(ctx, sqlID, scene, &observed, s.memes != nil)
			if err == nil {
				body, err = browserHistoryBody(result)
			}
		}
	case "history-invalid":
		response.status = 400
		body = []byte(`{"error":"Invalid request"}`)
	default:
		if exists {
			body, err = json.Marshal(owner.runtime.SceneState(sceneID))
		} else {
			response.status = 400
		}
	}
	if err != nil {
		fail()
		return
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
