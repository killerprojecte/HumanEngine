package externalcost

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const latestWhere = "id=? ORDER BY revision DESC LIMIT 1"

var (
	ErrConflict = errors.New("external_cost_conflict")
	ErrCapacity = errors.New("external_cost_capacity")
)

// ImportInput can be constructed only through the existing receipt validator.
// The original JSON number is retained for source-compatible signed-zero equality.
type ImportInput struct{ receipt Receipt }

func ParseImport(value any) (ImportInput, error) {
	probe, err := jsjson.NewObject(
		jsjson.Member{Key: "schemaVersion", Value: float64(1)},
		jsjson.Member{Key: "revision", Value: float64(1)},
		jsjson.Member{Key: "receipt", Value: value},
		jsjson.Member{Key: "correctionId", Value: nil},
		jsjson.Member{Key: "recordedAt", Value: "2000-01-01T00:00:00.000Z"},
	)
	if err != nil {
		return ImportInput{}, err
	}
	receipt, err := parseObject(probe)
	return ImportInput{receipt: receipt}, err
}

func Import(ctx context.Context, authority *recovery.Authority, main *sql.DB, input ImportInput) (Receipt, recovery.Head, error) {
	var result Receipt
	var head recovery.Head
	err := authority.WithWriteSession(ctx, main, func(session *recovery.WriteSession) error {
		var err error
		head, err = session.ReadyHead()
		if err != nil {
			return err
		}
		storage := Storage{DB: liveSQL{session}}
		exists, err := storage.AssertSupported(ctx)
		if err != nil {
			return err
		}
		receipt := input.receipt
		if exists {
			prior, err := storage.findSupported(ctx, latestWhere, receipt.ID())
			if err != nil {
				return err
			}
			if prior != nil {
				a, _ := prior.value.Get("receipt")
				b, _ := receipt.value.Get("receipt")
				if !equal(a, b) {
					return ErrConflict
				}
				result = *prior
				return nil
			}
			heads, err := storage.currentSupported(ctx)
			if err != nil {
				return err
			}
			if len(heads) >= 10000 {
				return ErrCapacity
			}
		}
		receipt.value = receipt.value.Clone()
		_ = receipt.value.Set("recordedAt", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
		intent, err := receipt.RecoveryEvent()
		if err != nil {
			return err
		}
		if err = session.RequireReader(10); err != nil {
			return err
		}
		head, err = session.AppendIntent(intent)
		if err != nil {
			return err
		}
		if !exists {
			if err = session.CreateExternalCostTable(); err != nil {
				return err
			}
		}
		if err = storage.putSupported(ctx, receipt); err != nil {
			return err
		}
		result = receipt
		return nil
	})
	if err != nil {
		return Receipt{}, head, err
	}
	return result, head, nil
}
