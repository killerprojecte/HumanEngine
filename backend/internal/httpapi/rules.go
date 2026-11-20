package httpapi

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func parseRules(input any) ([]any, bool) {
	rows, ok := input.([]any)
	if !ok {
		return nil, false
	}
	out := make([]any, len(rows))
	for i, item := range rows {
		row, ok := item.(*jsjson.Object)
		if !ok {
			return nil, false
		}
		result := new(jsjson.Object)
		for _, key := range []string{"id", "scope", "sceneId", "content", "nonOverridable", "enabled"} {
			value, present := row.Get(key)
			if key == "sceneId" && !present {
				continue
			}
			switch key {
			case "id", "sceneId":
				if _, ok := value.(string); !ok {
					return nil, false
				}
			case "scope":
				if value != "scene" && value != "global" {
					return nil, false
				}
			case "content":
				text, ok := value.(string)
				n, err := jsjson.CodePointCount(text)
				if !ok || err != nil || n < 1 || n > 5000 {
					return nil, false
				}
			default:
				if _, ok := value.(bool); !ok {
					return nil, false
				}
			}
			_ = result.Set(key, value)
		}
		scope, _ := result.Get("scope")
		scene, _ := result.Get("sceneId")
		if result.Len() != row.Len() || scope == "scene" && (scene == nil || scene == "") {
			return nil, false
		}
		out[i] = result
	}
	return out, true
}

func (s *Server) writeRules(ctx context.Context, input any, publish func(int, []byte) error) error {
	rows, valid := parseRules(input)
	if !valid {
		return publish(400, []byte(`{"error":"Invalid request"}`))
	}
	ids := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		value, _ := row.(*jsjson.Object).Get("id")
		id := value.(string)
		if _, duplicate := ids[id]; duplicate {
			return publish(400, []byte(`{"error":"duplicate_rule_ids"}`))
		}
		ids[id] = struct{}{}
	}
	body, err := jsjson.Marshal(rows)
	if err != nil {
		return err
	}
	if err := s.configuration.settings.SetRules(ctx, string(body)); err != nil {
		return err
	}
	return publish(200, body)
}
