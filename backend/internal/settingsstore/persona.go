package settingsstore

import (
	"context"
	"errors"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// RecordPersona mirrors the separate Store.set('personaVersions') performed
// before Store.set('settings'). It deliberately is not folded into the settings
// transaction: a later settings failure does not roll back the source history.
func (s *Store) RecordPersona(ctx context.Context, settings *jsjson.Object) error {
	schemas, e := contract.SharedSchemas()
	if e != nil {
		return e
	}
	_, e = s.Authority.PrepareWrite(ctx, s.Main, func(read *recovery.MainRead) (recovery.WritePlan, error) {
		previous, e := configValue(read, "settings", nil)
		if e != nil {
			return recovery.WritePlan{}, e
		}
		raw := contract.DefaultSettingsInput()
		if previous != nil {
			raw, e = jsjson.Marshal(previous)
			if e != nil {
				return recovery.WritePlan{}, e
			}
		}
		parsed, e := schemas.ParseOrdered("Settings", raw)
		if e != nil {
			return recovery.WritePlan{}, e
		}
		before, _ := parsed.Get("persona")
		after, _ := settings.Get("persona")
		noop := recovery.WritePlan{MinimumReader: 1, Apply: func(*recovery.MainMutation) error { return nil }}
		if before == after {
			return noop, nil
		}
		history, e := configValue(read, "personaVersions", []any{})
		if e != nil {
			return recovery.WritePlan{}, e
		}
		versions, ok := history.([]any)
		if !ok {
			return recovery.WritePlan{}, errors.New("persona_versions_invalid")
		}
		id, e := securityRevision()
		if e != nil {
			return recovery.WritePlan{}, e
		}
		version, _ := jsjson.NewObject(jsjson.Member{Key: "id", Value: id}, jsjson.Member{Key: "createdAt", Value: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, jsjson.Member{Key: "content", Value: after})
		versions = append(versions, version)
		encoded, e := jsjson.Stringify(versions)
		if e != nil {
			return recovery.WritePlan{}, e
		}
		return recovery.WritePlan{MinimumReader: 1, Apply: func(tx *recovery.MainMutation) error {
			_, e := tx.Exec("INSERT INTO main.config(key,value) VALUES('personaVersions',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encoded)
			return e
		}}, nil
	})
	return e
}
