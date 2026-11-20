package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type ModelProbeResult struct {
	OK         bool   `json:"ok"`
	Capability string `json:"capability"`
	Detail     string `json:"detail"`
	DurationMS int64  `json:"durationMs"`
}

type ModelProbe func(context.Context, string, string) (ModelProbeResult, error)

func (s *Server) WithModelProbe(probe ModelProbe) error {
	if s.configuration == nil || probe == nil {
		return errors.New("http_model_probe_dependencies_invalid")
	}
	s.configuration.probe = probe
	return nil
}

func modelProbePath(method, path string) (string, bool) {
	if method != http.MethodPost {
		return "", false
	}
	raw, ok := strings.CutPrefix(path, "/api/models/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(raw, "/probe")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return habitTryDecode(id, false), true
}

func (s *Server) executeModelProbe(ctx context.Context, id string, input any, publish func(int, []byte) error) error {
	if obj, ok := input.(*jsjson.Object); ok && obj.Len() == 1 {
		v, _ := obj.Get("capability")
		switch capability, _ := v.(string); capability {
		case "text", "tools", "vision", "embedding":
			result, err := s.configuration.probe(ctx, id, capability)
			if err != nil {
				return err
			}
			wire, err := json.Marshal(result)
			if err != nil {
				return err
			}
			return publish(http.StatusOK, wire)
		}
	}
	return publish(http.StatusBadRequest, []byte(`{"error":"Invalid request"}`))
}
