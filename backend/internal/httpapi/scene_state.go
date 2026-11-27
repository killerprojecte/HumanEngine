package httpapi

import (
	"context"
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
	fail := func(stage string) {
		_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "scene_" + operation + ".failed", Message: "Scene read failed", RequestID: id, Stage: stage})
		if !response.accepted && r.Context().Err() == nil {
			if r.Method == "HEAD" {
				w.WriteHeader(503)
			} else {
				habitError(w, 503, "Request failed; inspect server diagnostics", id)
			}
		}
	}
	if !available {
		fail("unavailable")
		return
	}
	ctx := r.Context()
	owner := s.runtimeStatus
	sqlID, err := jsjson.UTF8Text(sceneID)
	if err != nil {
		fail("invalid_scene_id")
		return
	}
	err = publishSceneRead(ctx, owner.publisher, operation == "history", func() (sceneReadSnapshot, error) {
		return owner.materializeSceneRead(ctx, operation, sqlID, sceneID, s.memes != nil)
	}, func(data []byte, status int) error {
		response.status = status
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return response.enqueueOwnedBytes(ctx, data)
	})
	if response.accepted {
		err = errors.Join(err, response.drain(ctx, w))
	}
	if err != nil {
		stage := "publication"
		if errors.Is(err, errSceneReadStale) {
			stage = "stale_snapshot"
		}
		fail(stage)
	}
}

type sceneReadSnapshot struct {
	observed recovery.Gate
	status   int
	body     []byte
}

var (
	errSceneReadStale    = errors.New("response_materialization_stale_or_unbound")
	errSceneReadAdvanced = errors.New("scene_history_head_advanced")
)

func publishSceneRead(ctx context.Context, publisher *recovery.ReadPublisher, local bool,
	materialize func() (sceneReadSnapshot, error), emit func([]byte, int) error,
) error {
	for {
		snapshot, err := materialize()
		if err != nil {
			if local && publisher.AwaitLocalIntent(ctx, err) {
				local = false
				continue
			}
			return err
		}
		err = publisher.PublishWithGate(ctx, func(_ *sql.Conn, current recovery.Gate) ([]byte, error) {
			observed := snapshot.observed
			if current == observed {
				return snapshot.body, nil
			}
			if local && current.Authority == observed.Authority && current.State == observed.State &&
				current.MinimumReader == observed.MinimumReader &&
				current.Head.InstanceID == observed.Head.InstanceID && current.Head.Sequence > observed.Head.Sequence {
				return nil, errSceneReadAdvanced
			}
			return nil, errSceneReadStale
		}, func(data []byte) error { return emit(data, snapshot.status) })
		if err == nil || !local || ctx.Err() != nil {
			return err
		}
		if !errors.Is(err, errSceneReadAdvanced) && !publisher.AwaitLocalIntent(ctx, err) {
			return err
		}
		// A recall committed during the local projection. Drop those bytes and
		// reproject once from the current authority. Never retry network work.
		local = false
	}
}

func (owner *runtimeStatusOwner) materializeSceneRead(ctx context.Context, operation, sqlID, sceneID string, mediaEnabled bool) (sceneReadSnapshot, error) {
	snapshot := sceneReadSnapshot{status: http.StatusOK}
	var exists bool
	var kind string
	var scene botruntime.Scene
	err := owner.publisher.PublishWithGate(ctx, func(q *sql.Conn, gate recovery.Gate) ([]byte, error) {
		snapshot.observed = gate
		switch operation {
		case "state":
			return nil, q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scenes WHERE id=?)", sqlID).Scan(&exists)
		case "history":
			return nil, q.QueryRowContext(ctx, "SELECT COALESCE((SELECT json_extract(value,'$.kind') FROM scenes WHERE id=?),'')", sqlID).Scan(&kind)
		case "history-remote":
			value, err := browserScene(ctx, q, sqlID)
			scene = value
			return nil, err
		}
		return nil, nil
	}, func([]byte) error { return nil })
	if err != nil {
		return sceneReadSnapshot{}, err
	}
	body := []byte(`{"error":"Scene unavailable"}`)
	switch operation {
	case "messages":
		body, err = sceneMessagesBody(owner.runtime, sceneID, mediaEnabled)
	case "history", "history-remote":
		body = []byte(`{"items":[],"coverage":"unavailable","reason":"real_scene_required"}`)
		if operation == "history" {
			if kind != "" && kind != "simulation" {
				var rows []*botruntime.Object
				var links map[*botruntime.Object]botruntime.DisplayImageLinks
				rows, links, err = owner.runtime.LocalHistory(sceneID, mediaEnabled)
				if err == nil {
					reason := "local_history_window"
					body, err = browserHistoryBody(botruntime.BrowserHistory{Items: rows, ImageLinks: links, Coverage: "partial", Reason: &reason})
				}
			}
		} else if scene.ID == sceneID && scene.Kind != "" && scene.Kind != "simulation" {
			var result botruntime.BrowserHistory
			result, err = owner.remoteHistory(ctx, sqlID, scene, &snapshot.observed, mediaEnabled)
			if err == nil {
				body, err = browserHistoryBody(result)
			}
		}
	case "history-invalid":
		snapshot.status = 400
		body = []byte(`{"error":"Invalid request"}`)
	default:
		if exists {
			body, err = json.Marshal(owner.runtime.SceneState(sceneID))
		} else {
			snapshot.status = 400
		}
	}
	snapshot.body = body
	return snapshot, err
}
