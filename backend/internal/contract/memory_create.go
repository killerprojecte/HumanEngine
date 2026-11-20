package contract

import (
	"errors"
	"regexp"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

var memoryDateTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]+)?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

// ParseMemoryCreate validates the frozen POST request, including Zod's
// calendar-date refinement. Preserve the ordered scope used for durable rows.
func (s *Schemas) ParseMemoryCreate(input any) (*jsjson.Object, error) {
	values, err := s.parseValue("MemoryCreate", input)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"validFrom", "validUntil", "occurredAt"} {
		if value, ok := values[key]; ok {
			text := value.(string)
			if !memoryDateTime.MatchString(text) {
				return nil, errors.New("invalid memory date-time")
			}
			if _, err := time.Parse("2006-01-02", text[:10]); err != nil {
				return nil, err
			}
		}
	}
	scope := values["scope"].(map[string]any)
	orderedScope := new(jsjson.Object)
	for _, key := range []string{"kind", "sceneId", "personId"} {
		if v, ok := scope[key]; ok {
			_ = orderedScope.Set(key, v)
		}
	}
	values["scope"] = orderedScope
	result := new(jsjson.Object)
	for _, key := range []string{"scope", "content", "kind", "importance", "confidence", "validFrom", "validUntil", "occurredAt", "epistemicKind", "locked"} {
		if v, ok := values[key]; ok {
			_ = result.Set(key, v)
		}
	}
	return result, nil
}
