package scenestore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Store struct {
	Main      *sql.DB
	Authority *recovery.Authority
}

// Save is the candidate transaction path, not a production HTTP endpoint. The
// caller supplies a schema-validated scene. Previous state, policy revision,
// durable revocation and main publication are coordinated by PrepareWrite.
func (s *Store) Save(ctx context.Context, input *jsjson.Object) (recovery.Head, error) {
	if s.Main == nil || s.Authority == nil || input == nil {
		return recovery.Head{}, errors.New("scene_store_invalid")
	}
	// Snapshot before lock acquisition so subsequent ownership is entirely local.
	raw, err := jsjson.Stringify(input)
	if err != nil {
		return recovery.Head{}, err
	}
	parsed, err := jsjson.Parse(raw)
	if err != nil {
		return recovery.Head{}, err
	}
	snapshot := parsed.(*jsjson.Object)
	id, ok := value(snapshot, "id").(string)
	if !ok {
		return recovery.Head{}, errors.New("scene_id_invalid")
	}
	// Match Node's SQLite binding boundary while keeping the original JS ID
	// in persisted JSON and authority/hash material. Distinct lone surrogates
	// intentionally share the same SQL key, exactly as the legacy binder does.
	sqlID, err := jsjson.UTF8Text(id)
	if err != nil {
		return recovery.Head{}, err
	}
	return s.Authority.PrepareWrite(ctx, s.Main, func(read *recovery.MainRead) (recovery.WritePlan, error) {
		rows, err := read.Query("SELECT value FROM main.scenes WHERE id=?", sqlID)
		if err != nil {
			return recovery.WritePlan{}, err
		}
		var previous *jsjson.Object
		if rows.Next() {
			var previousRaw string
			if err = rows.Scan(&previousRaw); err != nil {
				rows.Close()
				return recovery.WritePlan{}, err
			}
			decoded, parseErr := jsjson.Parse(previousRaw)
			if parseErr != nil {
				rows.Close()
				return recovery.WritePlan{}, parseErr
			}
			previous, ok = decoded.(*jsjson.Object)
			if !ok {
				rows.Close()
				return recovery.WritePlan{}, errors.New("scene_stored_value_invalid")
			}
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return recovery.WritePlan{}, err
		}
		plan, err := Prepare(previous, snapshot)
		if err != nil {
			return recovery.WritePlan{}, err
		}
		encoded, err := jsjson.Stringify(plan.Next)
		if err != nil {
			return recovery.WritePlan{}, err
		}
		intents := []recovery.Revocation{}
		if plan.Intent != nil {
			intents = append(intents, *plan.Intent)
		}
		return recovery.WritePlan{Intents: intents, MinimumReader: plan.MinimumReader, Apply: func(tx *recovery.MainMutation) error {
			_, err := tx.Exec("INSERT INTO main.scenes(id,value) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", sqlID, encoded)
			return err
		}}, nil
	})
}
