package installation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// EnsureBaseline is the owner-runtime boundary, not an offline replay primitive.
func EnsureBaseline(ctx context.Context, db *sql.DB, authority *recovery.Authority) error {
	return authority.EnsureInstallationBaseline(ctx, db, func(r *recovery.MainRead) ([]recovery.EligibilityChange, error) {
		rows, e := r.Query("SELECT EXISTS(SELECT 1 FROM main.sqlite_master WHERE type='table' AND name='memories')")
		if e != nil {
			return nil, e
		}
		var exists bool
		if !rows.Next() {
			rows.Close()
			return nil, errors.New("installation_census_failed")
		}
		e = rows.Scan(&exists)
		e = errors.Join(e, rows.Close())
		if e != nil {
			return nil, e
		}
		changes := []recovery.EligibilityChange{}
		if !exists {
			return changes, nil
		}
		rows, e = r.Query("SELECT value FROM main.memories ORDER BY id")
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				return nil, e
			}
			v, e := jsjson.Parse(raw)
			if e != nil {
				return nil, e
			}
			record, ok := v.(*jsjson.Object)
			if !ok {
				if v == nil {
					return nil, errors.New("recovery_eligibility_event_invalid")
				}
				continue
			}
			status, _ := record.Get("status")
			lineage, _ := record.Get("lineageStatus")
			if status != "invalidated" && lineage != "quarantined" {
				continue
			}
			id, _ := record.Get("id")
			idText, ok := id.(string)
			if !ok {
				return nil, errors.New("recovery_eligibility_event_invalid")
			}
			statusText, ok := status.(string)
			if !ok {
				return nil, errors.New("recovery_eligibility_event_invalid")
			}
			state := recovery.EligibilityState{Status: statusText}
			if lineage != nil {
				l, ok := lineage.(string)
				if !ok {
					return nil, errors.New("recovery_eligibility_event_invalid")
				}
				state.LineageStatus = &l
			}
			hash, e := assertions.EligibilityIncarnation(record)
			if e != nil {
				return nil, e
			}
			// Source canonical() joins undefined array items as empty slots, unlike null.
			if _, present := record.Get("learnedAt"); !present {
				idJSON, e := jsjson.Stringify(idText)
				if e != nil {
					return nil, e
				}
				sum := sha256.Sum256([]byte("[" + idJSON + ",]"))
				hash = hex.EncodeToString(sum[:])
			}
			changes = append(changes, recovery.EligibilityChange{ID: idText, IncarnationHash: hash, State: state})
		}
		return changes, rows.Err()
	})
}
