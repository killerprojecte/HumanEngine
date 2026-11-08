// Package httpapi serves the unchanged API contract without scheduling bot work.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"net/http"
	"sort"
	"strings"
)

type Server struct {
	modelCalls          *modelcalls.Store
	modelCallsPublisher *recovery.ReadPublisher
	diagnostics         DiagnosticReader
	diagnosticPublisher *recovery.ReadPublisher
	configuration       *configurationOwner
	memes               *memeengine.Service
	experience          *experienceservice.Service
	habits              *habitservice.Service
	main                *sql.DB
	authority           *recovery.Authority
	schemas             *contract.Schemas
	token               string
	logs                *observability.Logger
	owned               *ownership
}

func Open(ctx context.Context, database, token string, logs *observability.Logger) (*Server, error) {
	schemas, err := contract.SharedSchemas()
	if err != nil {
		return nil, err
	}
	main, err := storage.OpenExisting(database, false)
	if err != nil {
		return nil, err
	}
	var mode string
	if err = main.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		main.Close()
		return nil, errors.New("recovery_read_journal_unsupported")
	}
	gate, err := recovery.ReadGate(ctx, main)
	if err != nil {
		main.Close()
		return nil, err
	}
	authority, err := recovery.Open(ctx, gate.Authority)
	if err != nil {
		main.Close()
		return nil, err
	}
	return &Server{main: main, authority: authority, schemas: schemas, token: token, logs: logs, owned: &ownership{close: func() error { return errors.Join(authority.Close(), main.Close()) }}}, nil
}

// Close releases only resources acquired by Open. New borrows its dependencies.
func (s *Server) Close() error {
	if s.owned == nil {
		return nil
	}
	s.owned.once.Do(func() { s.owned.err = s.owned.close() })
	return s.owned.err
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.serveHTTP(w, r, true)
}

// ServeUnavailable preserves authentication, headers and route semantics without
// accessing dependencies after their owner has shut down.
func (s *Server) ServeUnavailable(w http.ResponseWriter, r *http.Request) {
	s.serveHTTP(w, r, false)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request, available bool) {
	id := r.Header.Get("X-Request-ID")
	if id == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		id = hex.EncodeToString(b)
	}
	w.Header().Set("X-Request-ID", id)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if (s.diagnostics != nil && diagnosticsPath(habitRoutingPath(r))) || (s.modelCalls != nil && modelCallsPath(habitRoutingPath(r))) {
		w.Header().Del("Cache-Control")
		w.Header().Del("X-Content-Type-Options")
	}
	if s.configuration != nil && configurationPath(habitRoutingPath(r)) {
		w.Header().Del("Cache-Control")
		w.Header().Del("X-Content-Type-Options")
	}
	authenticationError := func(status int, message string) {
		if (s.diagnostics != nil && diagnosticsPath(habitRoutingPath(r))) || (s.modelCalls != nil && modelCallsPath(habitRoutingPath(r))) {
			if r.Method == "HEAD" {
				w.WriteHeader(status)
			} else {
				habitError(w, status, message, "")
			}
			return
		}
		if s.configuration != nil && configurationPath(habitRoutingPath(r)) {
			if r.Method == "HEAD" {
				w.WriteHeader(status)
			} else {
				habitError(w, status, message, "")
			}
			return
		}
		if s.memes != nil && memePath(habitRoutingPath(r)) {
			if r.Method == "HEAD" {
				w.WriteHeader(status)
			} else {
				habitError(w, status, message, "")
			}
			return
		}
		if s.experience != nil && experiencePath(habitRoutingPath(r)) {
			if r.Method == "HEAD" {
				w.WriteHeader(status)
			} else {
				habitError(w, status, message, "")
			}
			return
		}
		respondError(w, status, message)
	}
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.token == "" {
		authenticationError(503, "Set HUMANENGINE_ADMIN_TOKEN on the server before using the API")
		return
	}
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(s.token)) != 1 {
		authenticationError(401, "Authentication required")
		return
	}
	if s.modelCalls != nil && modelCallsPath(habitRoutingPath(r)) {
		s.serveModelCalls(w, r, available, id)
		return
	}
	if s.diagnostics != nil && diagnosticsPath(habitRoutingPath(r)) {
		s.serveDiagnostics(w, r, available, id)
		return
	}
	if s.configuration != nil && configurationPath(habitRoutingPath(r)) {
		s.serveConfiguration(w, r, available, id)
		return
	}
	if s.memes != nil && memePath(habitRoutingPath(r)) {
		s.serveMemes(w, r, available, id)
		return
	}
	if s.experience != nil && experiencePath(habitRoutingPath(r)) {
		s.serveExperience(w, r, available, id)
		return
	}
	if s.habits != nil && habitPath(habitRoutingPath(r)) {
		s.serveHabits(w, r, available, id)
		return
	}
	if (r.Method != "GET" && r.Method != "HEAD") || (r.URL.Path != "/api/settings" && r.URL.Path != "/api/models" && r.URL.Path != "/api/scenes") {
		respondError(w, 503, "This API has not yet migrated to the Go preview")
		return
	}
	if !available {
		respondError(w, 503, "Request unavailable; outcome may be unknown. Do not automatically retry writes.")
		return
	}
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	err := s.publishResponse(r.Context(), r.URL.Path, response)
	// Enqueue is the publication point, just as ServerResponse.end(body) is in
	// the TypeScript host. Cleanup failure cannot undo an accepted response.
	// All transport methods, including WriteHeader, run after Publish releases
	// both SQLite snapshots, the main pool connection, and the authority mutex.
	if response.accepted {
		err = errors.Join(err, response.drain(r.Context(), w))
	}
	if err != nil {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "publication.failed", Message: err.Error(), RequestID: id, Fields: map[string]any{"path": r.URL.Path, "responseStarted": response.started, "responseQueued": response.accepted}})
		}
		if !response.accepted && r.Context().Err() == nil {
			respondError(w, 503, "Request unavailable; outcome may be unknown. Do not automatically retry writes.")
		}
	}
}
func respondError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func config(ctx context.Context, q *sql.Conn, key string, fallback []byte) ([]byte, error) {
	var value string
	err := q.QueryRowContext(ctx, "SELECT value FROM config WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return []byte(value), err
}
func (s *Server) projection(ctx context.Context, q *sql.Conn, path string) ([]byte, error) {
	switch path {
	case "/api/settings":
		raw, err := config(ctx, q, "settings", contract.DefaultSettingsInput())
		if err != nil {
			return nil, err
		}
		value, err := s.schemas.Parse("Settings", raw)
		if err != nil {
			return nil, err
		}
		return marshalProjection(value)
	case "/api/models":
		raw, err := config(ctx, q, "models", []byte(`{"providers":[],"models":[],"routes":[]}`))
		if err != nil {
			return nil, err
		}
		value, err := s.schemas.Parse("ModelConfig", raw)
		if err != nil {
			return nil, err
		}
		for _, entry := range value["routes"].([]any) {
			route := entry.(map[string]any)
			if route["role"] == "thinker" {
				route["role"] = "replyer"
			}
		}
		for _, entry := range value["providers"].([]any) {
			provider := entry.(map[string]any)
			var exists bool
			err = q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM secrets WHERE namespace=? AND name=?)", "model", provider["id"]).Scan(&exists)
			if err != nil {
				return nil, err
			}
			provider["credentialConfigured"] = exists
		}
		return marshalProjection(value)
	case "/api/scenes":
		rows, err := q.QueryContext(ctx, "SELECT value FROM scenes ORDER BY id")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		values := []any{}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				return nil, err
			}
			value, err := jsjson.Parse(raw)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
		return jsjson.Marshal(values)
	}
	return nil, fmt.Errorf("unknown config projection %s", path)
}

// HTTP object key order is not contractual. This local conversion explicitly
// chooses sorted keys and preserves canonical WTF-8 values through jsjson.
// Never use it for persistence, recovery payloads, or JSON.stringify hash input:
// the schema-validation maps have already discarded source insertion order.
func marshalProjection(value any) ([]byte, error) {
	converted, err := projectionJSON(value)
	if err != nil {
		return nil, err
	}
	return jsjson.Marshal(converted)
}

func projectionJSON(value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := new(jsjson.Object)
		for _, key := range keys {
			child, err := projectionJSON(v[key])
			if err != nil {
				return nil, err
			}
			if err = out.Set(key, child); err != nil {
				return nil, err
			}
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			var err error
			out[i], err = projectionJSON(child)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	default:
		return value, nil
	}
}
