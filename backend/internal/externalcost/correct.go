package externalcost

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"regexp"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const correctionWhere = "correction_id=?"

var (
	ErrRevisionConflict = errors.New("external_cost_revision_conflict")
	errCorrectionInput  = errors.New("external_cost_correction_invalid")
	correctionID        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
)

type CorrectionInput struct {
	replacement Receipt
	id          string
	expected    int64
}

func ParseCorrection(value any) (CorrectionInput, error) {
	obj, ok := value.(*jsjson.Object)
	if !ok || obj.Len() != 4 {
		return CorrectionInput{}, errCorrectionInput
	}
	rawID, _ := obj.Get("id")
	rawCorrection, _ := obj.Get("correctionId")
	rawExpected, _ := obj.Get("expectedRevision")
	replacement, _ := obj.Get("replacement")
	expected, hasExpected := rawExpected.(float64)
	correction, hasCorrection := rawCorrection.(string)
	if !hasCorrection || !correctionID.MatchString(correction) || !hasExpected || !(expected >= 1 && expected <= 9007199254740991 && math.Trunc(expected) == expected) {
		return CorrectionInput{}, errCorrectionInput
	}
	parsed, err := ParseImport(replacement)
	if err != nil || rawID != parsed.receipt.ID() {
		return CorrectionInput{}, errCorrectionInput
	}
	return CorrectionInput{replacement: parsed.receipt, id: correction, expected: int64(expected)}, nil
}

func Correct(ctx context.Context, authority *recovery.Authority, main *sql.DB, input CorrectionInput) (Receipt, recovery.Head, error) {
	return writeCost(ctx, authority, main, func(storage Storage, exists bool) (Receipt, bool, error) {
		if !exists {
			return Receipt{}, false, ErrRevisionConflict
		}
		existing, err := storage.findSupported(ctx, correctionWhere, input.id)
		if err != nil {
			return Receipt{}, false, err
		}
		if existing != nil {
			if int64(existing.Revision()) != input.expected+1 || !existing.sameInput(input.replacement) {
				return Receipt{}, false, ErrConflict
			}
			return *existing, false, nil
		}
		prior, err := storage.findSupported(ctx, latestWhere, input.replacement.ID())
		if err != nil {
			return Receipt{}, false, err
		}
		if prior == nil || int64(prior.Revision()) != input.expected {
			return Receipt{}, false, ErrRevisionConflict
		}
		receipt := Receipt{value: input.replacement.value.Clone()}
		_ = receipt.value.Set("revision", float64(prior.Revision()+1))
		_ = receipt.value.Set("correctionId", input.id)
		return receipt, true, nil
	})
}
