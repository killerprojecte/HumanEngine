// Package typingdelay implements the builtin policy inside the WASM artifact.
package typingdelay

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/clipperhouse/uax29/v2/graphemes"
	"github.com/killerprojecte/HumanEngine/backend/plugins/metadata"
	"math"
	"strings"
)

type Configuration struct {
	SpeedFactor *float64 `json:"speedFactor"`
}
type Part struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type Segment struct {
	Parts []Part `json:"parts"`
}

func Delay(segment Segment, raw json.RawMessage) (int, error) {
	config := Configuration{}
	if len(raw) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return 0, errors.New("invalid_typing_delay_configuration")
		}
		if value, exists := object["speedFactor"]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return 0, errors.New("invalid_typing_delay_configuration")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return 0, err
		}
	}
	speed := 1.0
	if config.SpeedFactor != nil {
		speed = *config.SpeedFactor
	}
	if speed < 0.1 || speed > 10 || math.IsNaN(speed) {
		return 0, errors.New("invalid_typing_delay_configuration")
	}
	var text strings.Builder
	for _, part := range segment.Parts {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	iterator := graphemes.FromString(text.String())
	count := 0
	for iterator.Next() {
		count++
	}
	// Existing policy, not a new resource cap: preserve the legacy 30-second timing.
	return int(math.Min(30000, math.Floor((300+float64(count)*200)/speed+0.5))), nil
}
func Invoke(input []byte) any {
	var in struct {
		Operation string          `json:"operation"`
		Config    json.RawMessage `json:"config"`
		Segment   Segment         `json:"segment"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return map[string]any{"error": "invalid_plugin_input"}
	}
	switch in.Operation {
	case "manifest":
		return metadata.Manifest("typing-delay")
	case "delay":
		ms, err := Delay(in.Segment, in.Config)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"delayMs": ms}
	default:
		return map[string]any{"error": "unknown_plugin_operation"}
	}
}
