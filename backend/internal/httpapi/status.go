package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

type runtimeStatusOwner struct {
	runtime       *botruntime.Runtime
	scheduler     *scheduler.Scheduler
	publisher     *recovery.ReadPublisher
	vectorVersion string
	startedAt     time.Time
}

func (s *Server) WithRuntimeStatus(runtime *botruntime.Runtime, schedule *scheduler.Scheduler, publisher *recovery.ReadPublisher, vectorVersion string, startedAt time.Time) error {
	if s == nil || runtime == nil || schedule == nil || s.logs == nil || !publisher.OwnedBy(s.main, s.authority) || vectorVersion == "" || startedAt.IsZero() {
		return errors.New("http_status_dependencies_invalid")
	}
	s.runtimeStatus = &runtimeStatusOwner{runtime, schedule, publisher, vectorVersion, startedAt}
	return nil
}
func statusPath(path string) bool {
	return path == "/api/status" || strings.HasPrefix(path, "/api/status/")
}

type statusSnapshot struct {
	Name                 string                     `json:"name"`
	StartedAt            string                     `json:"startedAt"`
	UptimeSeconds        int64                      `json:"uptimeSeconds"`
	SceneCount           int                        `json:"sceneCount"`
	EnabledScenes        int                        `json:"enabledScenes"`
	ProviderCount        int                        `json:"providerCount"`
	ModelCount           int                        `json:"modelCount"`
	ActiveTasks          int                        `json:"activeTasks"`
	RecentErrors         int                        `json:"recentErrors"`
	VectorVersion        string                     `json:"vectorVersion"`
	Version              string                     `json:"version"`
	QueuedModelRequests  int                        `json:"queuedModelRequests"`
	RunningModelRequests int                        `json:"runningModelRequests"`
	Adapters             []botruntime.AdapterStatus `json:"adapters"`
}

func (s *Server) statusConfiguration(ctx context.Context, q *sql.Conn, result *statusSnapshot) error {
	var settings struct{ Name string }
	var models struct{ Providers, Models []json.RawMessage }
	var scenes []struct{ ParticipationEnabled bool }
	for _, entry := range []struct {
		path   string
		target any
	}{{"/api/settings", &settings}, {"/api/models", &models}, {"/api/scenes", &scenes}} {
		data, err := s.projection(ctx, q, entry.path)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, entry.target); err != nil {
			return err
		}
	}
	result.Name = settings.Name
	result.SceneCount = len(scenes)
	for _, scene := range scenes {
		if scene.ParticipationEnabled {
			result.EnabledScenes++
		}
	}
	result.ProviderCount = len(models.Providers)
	result.ModelCount = len(models.Models)
	return nil
}
func (s *Server) serveRuntimeStatus(w http.ResponseWriter, r *http.Request, available bool, id string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	fail := func() {
		_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "status.failed", Message: "Status read failed", RequestID: id})
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
	owner := s.runtimeStatus
	result := statusSnapshot{}
	err := owner.publisher.Publish(r.Context(), func(q *sql.Conn) ([]byte, error) {
		if habitRoutingPath(r) != "/api/status" || (r.Method != "GET" && r.Method != "HEAD") {
			return nil, nil
		}
		return nil, s.statusConfiguration(r.Context(), q, &result)
	}, func([]byte) error { return nil })
	if err != nil {
		fail()
		return
	}
	var body []byte
	if habitRoutingPath(r) != "/api/status" || (r.Method != "GET" && r.Method != "HEAD") {
		response.status = 404
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
		body = []byte("404 Not Found")
	} else {
		result.StartedAt = owner.startedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		result.UptimeSeconds = int64(math.Floor(float64(time.Now().UnixMilli()-owner.startedAt.UnixMilli()) / 1000))
		result.ActiveTasks = owner.runtime.ActiveTasks()
		result.RecentErrors = s.logs.Retention().RecentErrors
		result.VectorVersion = owner.vectorVersion
		result.Version = "0.1.0"
		result.QueuedModelRequests = owner.scheduler.Status().Queued
		result.RunningModelRequests = owner.scheduler.Status().Running
		// Never call external getters under database publication or runtime locks.
		result.Adapters, err = owner.runtime.AdapterStatuses(r.Context())
		if err != nil {
			fail()
			return
		}
		body, err = json.Marshal(result)
		if err != nil {
			fail()
			return
		}
	}
	err = owner.publisher.Publish(r.Context(), func(*sql.Conn) ([]byte, error) { return body, nil }, func(data []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return response.enqueue(r.Context(), data)
	})
	if response.accepted {
		err = errors.Join(err, response.drain(r.Context(), w))
	}
	if err != nil {
		fail()
	}
}
