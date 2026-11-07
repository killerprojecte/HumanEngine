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
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"net/http"
	"strings"
)

type Server struct {
	main      *sql.DB
	authority *recovery.Authority
	schemas   *contract.Schemas
	token     string
	logs      *observability.Logger
}

func Open(ctx context.Context, database, token string, logs *observability.Logger) (*Server, error) {
	schemas, err := contract.NewSchemas()
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
	return &Server{main: main, authority: authority, schemas: schemas, token: token, logs: logs}, nil
}
func (s *Server) Close() error { return errors.Join(s.authority.Close(), s.main.Close()) }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.token == "" {
		respondError(w, 503, "Set HUMANENGINE_ADMIN_TOKEN on the server before using the API")
		return
	}
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(s.token)) != 1 {
		respondError(w, 401, "Authentication required")
		return
	}
	if (r.Method != "GET" && r.Method != "HEAD") || (r.URL.Path != "/api/settings" && r.URL.Path != "/api/models" && r.URL.Path != "/api/scenes") {
		respondError(w, 503, "This API has not yet migrated to the Go preview")
		return
	}
	emitted := false
	err := s.authority.Publish(r.Context(), s.main, func(conn *sql.Conn) ([]byte, error) { return s.projection(r.Context(), conn, r.URL.Path) }, func(body []byte) error {
		emitted = true
		w.WriteHeader(http.StatusOK)
		if r.Method == "HEAD" {
			return nil
		}
		_, err := w.Write(body)
		return err
	})
	if err != nil {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "publication.failed", Message: err.Error(), RequestID: id, Fields: map[string]any{"path": r.URL.Path, "responseStarted": emitted}})
		}
		if !emitted {
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
		return json.Marshal(value)
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
		return json.Marshal(value)
	case "/api/scenes":
		rows, err := q.QueryContext(ctx, "SELECT value FROM scenes ORDER BY id")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		values := []json.RawMessage{}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				return nil, err
			}
			if !json.Valid([]byte(raw)) {
				return nil, errors.New("invalid scene JSON")
			}
			values = append(values, json.RawMessage(raw))
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
		return json.Marshal(values)
	}
	return nil, fmt.Errorf("unknown config projection %s", path)
}
