package observability

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// retainedProjection is separate from JSON/human sink encoding. Fields are the
// already captured snapshot, so custom marshalers are never called twice.
// Native Event correlation fields supply flow context; Fields supplies the
// source context spread and can override it. Source always regenerates message.
func retainedProjection(e Event, fields json.RawMessage) ([]byte, string, error) {
	row, _ := jsjson.NewObject(
		jsjson.Member{Key: "id", Value: e.ID},
		jsjson.Member{Key: "at", Value: e.At.UTC().Format("2006-01-02T15:04:05.000Z")},
		jsjson.Member{Key: "level", Value: e.Level},
		jsjson.Member{Key: "module", Value: e.Module},
		jsjson.Member{Key: "code", Value: e.Code},
	)
	for _, entry := range []struct{ key, value string }{
		{"requestId", e.RequestID}, {"traceId", e.TraceID}, {"sceneId", e.SceneID},
		{"turnId", e.TurnID}, {"stage", e.Stage},
	} {
		if entry.value != "" {
			row.Set(entry.key, entry.value)
		}
	}
	if e.DurationMS != 0 {
		row.Set("durationMs", e.DurationMS)
	}
	if len(fields) > 0 {
		value, err := jsjson.ParseBytes(fields)
		if err != nil {
			return nil, "", err
		}
		if context := retainedObject(value); context != nil {
			for _, member := range context.Members() {
				row.Set(member.Key, member.Value)
			}
		}
	}
	if retainedGet(row, "role") == "thinker" {
		row.Set("role", "replyer")
	}
	row.Set("message", retainedReadable(row))
	encoded, err := jsjson.Marshal(row)
	if err != nil {
		return nil, "", err
	}
	return append(encoded, '\n'), retainedText(retainedGet(row, "level")), nil
}
