package memoryservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"math"
)

type semanticRows interface {
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
}
type semanticState struct {
	settings map[string]any
	revision uint64
	scene    *Object
}

func semanticReadState(query func(string, ...any) (semanticRows, error), scene string) (out semanticState, err error) {
	read := func(sql string, args ...any) (string, bool, error) {
		rows, e := query(sql, args...)
		if e != nil {
			return "", false, e
		}
		defer rows.Close()
		var raw string
		exists := rows.Next()
		if exists {
			if e = rows.Scan(&raw); e != nil {
				return "", false, e
			}
		}
		return raw, exists, rows.Err()
	}
	raw, exists, e := read("SELECT value FROM config WHERE key=?", "settings")
	if e != nil {
		return out, e
	}
	if !exists {
		raw = string(contract.DefaultSettingsInput())
	}
	schemas, e := contract.NewSchemas()
	if e != nil {
		return out, e
	}
	out.settings, e = schemas.Parse("Settings", []byte(raw))
	if e != nil {
		return out, e
	}
	raw, exists, e = read("SELECT value FROM config WHERE key=?", "memory-semantic-policy-revision")
	if e != nil {
		return out, e
	}
	if exists {
		v, e := jsjson.Parse(raw)
		if e != nil {
			return out, e
		}
		n, ok := v.(float64)
		if !ok || math.IsNaN(n) || n < 0 || n > 9007199254740991 || math.Trunc(n) != n {
			return out, errors.New("semantic_policy_invalid")
		}
		out.revision = uint64(n)
	}
	if scene != "" {
		raw, exists, e = read("SELECT value FROM scenes WHERE id=?", scene)
		if e != nil {
			return out, e
		}
		if exists {
			v, e := jsjson.Parse(raw)
			if e != nil {
				return out, e
			}
			out.scene = object(v)
		}
	}
	return out, nil
}
func semanticSessionState(ws *recovery.WriteSession, scene string) (semanticState, error) {
	if ws == nil {
		return semanticState{}, errors.New("semantic_transaction_required")
	}
	return semanticReadState(func(q string, args ...any) (semanticRows, error) { return ws.Query(q, args...) }, scene)
}
func (s semanticState) enabled() bool {
	return s.settings["memorySemanticEnabled"] == true && truthy(value(s.scene, "learningEnabled"))
}
func (s semanticState) admission() semantic.AdmissionPolicy {
	return semantic.AdmissionPolicy{Enabled: s.settings["memorySemanticEnabled"] == true, LearningEnabled: truthy(value(s.scene, "learningEnabled")), AutoSummary: s.settings["memorySemanticAutoAdmissionEnabled"] == true}
}
func (s semanticState) identity() (string, error) {
	revision := value(s.scene, "memoryReadRevision")
	if revision == nil {
		revision = float64(0)
	}
	return jsjson.Stringify([]any{float64(s.revision), revision, value(s.scene, "learningEnabled")})
}
