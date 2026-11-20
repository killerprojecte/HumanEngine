package settingsstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// ErrSecuritySchemaUnsupported refuses to reuse an incompatible existing
// security-state object. The canonical table may be absent on a fresh install.
var ErrSecuritySchemaUnsupported = errors.New("settings_security_schema_unsupported")

type Store struct {
	Main      *sql.DB
	Authority *recovery.Authority
}

// Set persists a JSON-domain settings value. It intentionally does not apply
// SettingsSchema: the source store only validates the budget security projection.
// Values follow jsjson's lossless data model; nil means JSON null. Callers own
// input until this call has taken its snapshot and must not mutate it concurrently.
// A failure can leave durable authority intent. Never automatically retry it.
func (s *Store) Set(ctx context.Context, input any) (recovery.Head, error) {
	if s == nil || s.Main == nil || s.Authority == nil {
		return recovery.Head{}, errors.New("settings_store_invalid")
	}
	encoded, err := jsjson.Stringify(input)
	if err != nil {
		return recovery.Head{}, err
	}
	snapshot := clone(input)
	return s.Authority.PrepareOrderedWrite(ctx, s.Main, func(read *recovery.MainRead) (recovery.OrderedWritePlan, error) {
		previous, err := configValue(read, "settings", nil)
		if err != nil {
			return recovery.OrderedWritePlan{}, err
		}
		prepared, err := prepare(previous, snapshot, func(key string) (any, error) { return configValue(read, key, float64(0)) })
		if err != nil {
			return recovery.OrderedWritePlan{}, err
		}
		revision, createSchema := "", false
		if prepared.securityChanged {
			// Reject an incompatible existing object before any earlier flag
			// intent. Absent is valid; create only at the source's security step.
			createSchema, err = securitySchemaMissing(read)
			if err != nil {
				return recovery.OrderedWritePlan{}, err
			}
			revision, err = securityRevision()
			if err != nil {
				return recovery.OrderedWritePlan{}, err
			}
		}
		ordered := recovery.OrderedWritePlan{MinimumReader: 1}
		intents := prepared.intents
		updates := prepared.updates
		applyUpdates := func(tx *recovery.MainMutation) error {
			for _, update := range updates {
				if _, err := tx.Exec("INSERT OR REPLACE INTO main.config(key,value) VALUES(?,?)", update.key, update.raw); err != nil {
					return err
				}
			}
			return nil
		}
		// Revision SQL precedes flags and lazy schema work. With neither, it
		// can run inside the normal final callback instead of a separate step.
		if len(updates) != 0 && (len(intents) != 0 || revision != "") {
			ordered.Steps = append(ordered.Steps, recovery.OrderedWriteStep{Apply: applyUpdates})
		}
		if revision != "" {
			if len(intents) != 0 {
				ordered.Steps = append(ordered.Steps, recovery.OrderedWriteStep{Intents: intents, Apply: func(*recovery.MainMutation) error { return nil }})
			}
			intents = []recovery.Revocation{recovery.SecurityState{Revision: revision}}
		}
		ordered.Steps = append(ordered.Steps, recovery.OrderedWriteStep{EnsureSecurityState: createSchema, Intents: intents, Apply: func(tx *recovery.MainMutation) error {
			if len(intents) == 0 {
				if err := applyUpdates(tx); err != nil {
					return err
				}
			}
			if prepared.afterFlagsError != nil {
				return prepared.afterFlagsError
			}
			if revision != "" {
				if _, err := tx.Exec("INSERT INTO main.recovery_security_state VALUES(1,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision", revision); err != nil {
					return err
				}
			}
			_, err := tx.Exec("INSERT INTO main.config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "settings", encoded)
			return err
		}})
		return ordered, nil
	})
}

func configValue(read *recovery.MainRead, key string, fallback any) (any, error) {
	rows, err := read.Query("SELECT value FROM main.config WHERE key=?", key)
	if err != nil {
		return nil, err
	}
	var raw string
	exists := rows.Next()
	if exists {
		err = rows.Scan(&raw)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	if !exists {
		return fallback, nil
	}
	return jsjson.Parse(raw)
}

// securitySchemaMissing distinguishes fresh enrollment from an unsupported
// preexisting object. This runs under the same main writer transaction as the
// later scoped EnsureSecurityState; no schema changes are exposed to callers.
func securitySchemaMissing(read *recovery.MainRead) (bool, error) {
	rows, err := read.Query("SELECT type,sql FROM main.sqlite_master WHERE name='recovery_security_state'")
	if err != nil {
		return false, err
	}
	var kind, definition string
	exists := rows.Next()
	if exists {
		err = rows.Scan(&kind, &definition)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || !exists {
		return !exists, err
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(definition), ""))
	normalized = strings.Replace(normalized, "ifnotexists", "", 1)
	const expected = "createtablerecovery_security_state(idintegerprimarykeycheck(id=1),revisiontextnotnull)"
	if kind != "table" || normalized != expected {
		return false, ErrSecuritySchemaUnsupported
	}
	return false, nil
}
