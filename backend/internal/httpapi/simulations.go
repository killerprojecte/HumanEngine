package httpapi

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const simulationsPrefix = "/api/simulations"

func simulationsPath(path string) bool {
	return path == simulationsPrefix || strings.HasPrefix(path, simulationsPrefix+"/")
}

func (s *Server) serveSimulations(w http.ResponseWriter, r *http.Request, available bool, requestID, path string) {
	ctx := r.Context()
	response := newPublicationResponse(w.Header(), r.Method == http.MethodHead)
	response.header.Set("Cache-Control", "no-store")
	response.header.Set("X-Content-Type-Options", "nosniff")
	fail := func(err error) {
		_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "simulations.failed", Message: err.Error(), RequestID: requestID})
		if !response.accepted && ctx.Err() == nil {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if response.head {
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				habitError(w, http.StatusServiceUnavailable, "Request failed; inspect server diagnostics", requestID)
			}
		}
	}
	if !available {
		fail(errors.New("simulation_request_unavailable"))
		return
	}

	// Never hold the publication reservation across a simulation turn.
	owner := s.runtimeStatus
	var observed recovery.Gate
	err := owner.publisher.Publish(ctx, func(q *sql.Conn) ([]byte, error) {
		var err error
		observed, err = recovery.ReadGate(ctx, q)
		return nil, err
	}, func([]byte) error { return nil })
	if err != nil {
		fail(err)
		return
	}

	status, body, err := simulationResponse(owner.runtime, r, path)
	if err != nil {
		fail(err)
		return
	}
	response.status = status
	if status == http.StatusNotFound {
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
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
	}, func(data []byte) error { return response.enqueue(ctx, data) })
	if response.accepted {
		err = errors.Join(err, response.drain(ctx, w))
	}
	if err != nil {
		fail(err)
	}
}

func simulationResponse(runtime *botruntime.Runtime, r *http.Request, path string) (int, []byte, error) {
	segment, operation, nested := strings.Cut(strings.TrimPrefix(path, simulationsPrefix+"/"), "/")
	id := habitTryDecode(segment, false)
	switch {
	case path == simulationsPrefix && r.Method == http.MethodPost:
		id, err := runtime.CreateSimulation()
		if err != nil {
			return 0, nil, err
		}
		result := new(jsjson.Object)
		_ = result.Set("id", id)
		return simulationJSON(result)
	case segment != "" && !nested && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		rows, err := runtime.SimulationMessages(id)
		if err != nil {
			return 0, nil, err
		}
		if rows == nil {
			return http.StatusBadRequest, []byte(`{"error":"Simulation unavailable"}`), nil
		}
		return simulationMessagesJSON(rows)
	case segment != "" && !nested && r.Method == http.MethodDelete:
		runtime.EndSimulation(id)
		return http.StatusOK, []byte(`{"ok":true}`), nil
	case segment != "" && operation == "messages" && r.Method == http.MethodPost:
		text, err := simulationText(r)
		if err != nil {
			return 0, nil, err
		}
		if text == "" {
			return http.StatusBadRequest, []byte(`{"error":"Invalid request"}`), nil
		}
		rows, err := runtime.Simulate(r.Context(), id, text)
		if err != nil {
			return 0, nil, err
		}
		return simulationMessagesJSON(rows)
	default:
		return http.StatusNotFound, []byte("404 Not Found"), nil
	}
}

func simulationText(r *http.Request) (string, error) {
	if !habitJSONContentType.MatchString(r.Header.Get("Content-Type")) {
		return "", nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	value, err := jsjson.ParseBytes(raw)
	if err != nil {
		return "", err
	}
	input, ok := value.(*jsjson.Object)
	if !ok || input.Len() != 1 {
		return "", nil
	}
	value, _ = input.Get("text")
	text, ok := value.(string)
	if !ok {
		return "", nil
	}
	length, err := jsjson.CodePointCount(text)
	if err != nil || length > 12000 {
		return "", err
	}
	return text, nil
}

func simulationMessagesJSON(rows []*botruntime.Object) (int, []byte, error) {
	out := make([]any, len(rows))
	for i, row := range rows {
		out[i] = row
	}
	return simulationJSON(out)
}

func simulationJSON(value any) (int, []byte, error) {
	raw, err := jsjson.Marshal(value)
	return http.StatusOK, raw, err
}
