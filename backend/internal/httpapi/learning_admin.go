package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/learningruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type learningAdminOwner struct {
	coordinator *learningruntime.Coordinator
	publisher   *recovery.ReadPublisher
}

func (s *Server) WithLearningAdmin(coordinator *learningruntime.Coordinator, publisher *recovery.ReadPublisher) error {
	if coordinator == nil || !publisher.OwnedBy(s.main, s.authority) {
		return errors.New("http_learning_admin_owner_invalid")
	}
	s.learningAdmin = &learningAdminOwner{coordinator, publisher}
	return nil
}

func learningAdminPath(path string) bool {
	return path == "/api/learning" || strings.HasPrefix(path, "/api/learning/")
}

type learningOperation uint8

const (
	learningNotFound learningOperation = iota
	learningStatus
	learningRetry
	learningDiscard
)

func learningAdminRoute(method, path string) (learningOperation, string) {
	if path == "/api/learning/status" && (method == "GET" || method == "HEAD") {
		return learningStatus, ""
	}
	raw, found := strings.CutPrefix(path, "/api/learning/failures/")
	if !found {
		return learningNotFound, ""
	}
	var action learningOperation
	switch method {
	case "POST":
		raw, found = strings.CutSuffix(raw, "/retry")
		if !found {
			return learningNotFound, ""
		}
		action = learningRetry
	case "DELETE":
		action = learningDiscard
	default:
		return learningNotFound, ""
	}
	if raw == "" || strings.Contains(raw, "/") {
		return learningNotFound, ""
	}
	return action, habitTryDecode(raw, false)
}

func (s *Server) serveLearningAdmin(w http.ResponseWriter, r *http.Request, available bool, path, requestID string) {
	ctx := r.Context()
	owner := s.learningAdmin
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	report := func(stage string) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "learning_admin.failed", Message: "Learning administration request failed", Stage: stage, RequestID: requestID})
		}
		if !response.accepted && ctx.Err() == nil {
			if r.Method == "HEAD" {
				w.WriteHeader(503)
			} else {
				habitError(w, 503, "Request failed; inspect server diagnostics", requestID)
			}
		}
	}
	if !available {
		report("request_admission")
		return
	}

	operation, id := learningAdminRoute(r.Method, path)
	var body []byte
	if operation == learningRetry || operation == learningDiscard {
		// Match the authenticated source readiness check before action. The
		// coordinator can read authority on retry, so do not hold a read
		// publication across it. Publish only after the action returns.
		err := owner.publisher.Publish(ctx, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
		if err != nil {
			report("recovery_readiness")
			return
		}
		var ok bool
		switch operation {
		case learningRetry:
			ok, err = owner.coordinator.Retry(ctx, id)
			body = []byte(`{"error":"Batch is unavailable or cannot safely retry"}`)
		case learningDiscard:
			ok = owner.coordinator.Discard(id)
			body = []byte(`{"error":"Batch is unavailable"}`)
		}
		if err != nil {
			report("failure_action")
			return
		}
		if ok {
			body = []byte(`{"ok":true}`)
		} else {
			response.status = http.StatusBadRequest
		}
	}
	err := owner.publisher.Publish(ctx, func(*sql.Conn) ([]byte, error) {
		switch operation {
		case learningStatus:
			return json.Marshal(owner.coordinator.Status())
		case learningNotFound:
			response.status = http.StatusNotFound
			response.header.Set("Content-Type", "text/plain; charset=UTF-8")
			return []byte("404 Not Found"), nil
		default:
			return body, nil
		}
	}, func(body []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return response.enqueue(ctx, body)
	})
	if response.accepted {
		err = errors.Join(err, response.drain(ctx, w))
	}
	if err != nil {
		report("response_publication")
	}
}
