package settingsstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// ErrSecuritySchemaUnsupported identifies a candidate enrollment gap, not an
// original Store validation error. Bootstrap/migration must establish the TS
// security-state table before security-changing writes can use this service.
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
	return s.Authority.PrepareWrite(ctx, s.Main, func(read *recovery.MainRead) (recovery.WritePlan, error) {
		previous, err := configValue(read, "settings", nil)
		if err != nil {
			return recovery.WritePlan{}, err
		}
		prepared, err := prepare(previous, snapshot, func(key string) (any, error) { return configValue(read, key, float64(0)) })
		if err != nil {
			return recovery.WritePlan{}, err
		}
		revision := ""
		if prepared.securityChanged {
			// This unsupported enrollment state is detected before any intent. Do not
			// route CREATE through MainMutation or partially change an unenrolled DB.
			if err = requireSecuritySchema(read); err != nil {
				return recovery.WritePlan{}, err
			}
			revision, err = securityRevision()
			if err != nil {
				return recovery.WritePlan{}, err
			}
			prepared.intents = append(prepared.intents, recovery.SecurityState{Revision: revision})
		}
		return recovery.WritePlan{Intents: prepared.intents, MinimumReader: 1, Apply: func(tx *recovery.MainMutation) error {
			for _, update := range prepared.updates {
				if _, err := tx.Exec("INSERT OR REPLACE INTO main.config(key,value) VALUES(?,?)", update.key, update.raw); err != nil {
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
		}}, nil
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

// Accept the canonical schema created by ensureSecurityState. No Go schema
// migration is smuggled into the restricted main mutation capability. Other
// schemas (even seemingly similar ones) need a separately audited migration.
func requireSecuritySchema(read *recovery.MainRead) error {
	rows, err := read.Query("SELECT type,sql FROM main.sqlite_master WHERE name='recovery_security_state'")
	if err != nil {
		return err
	}
	var kind, definition string
	exists := rows.Next()
	if exists {
		err = rows.Scan(&kind, &definition)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(definition), ""))
	normalized = strings.Replace(normalized, "ifnotexists", "", 1)
	const expected = "createtablerecovery_security_state(idintegerprimarykeycheck(id=1),revisiontextnotnull)"
	if !exists || kind != "table" || normalized != expected {
		return ErrSecuritySchemaUnsupported
	}
	return nil
}
