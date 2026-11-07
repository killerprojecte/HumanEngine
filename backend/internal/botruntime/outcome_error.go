package botruntime

import "github.com/killerprojecte/HumanEngine/backend/internal/dispatch"

// OperationOutcomesError preserves trusted outcome causes in receipt order. The
// public error text and receipt JSON never include arbitrary provider contents.
type OperationOutcomesError struct{ Failures []*dispatch.Failure }

func (*OperationOutcomesError) Error() string { return "operation_outcomes_failed" }
func (e *OperationOutcomesError) Unwrap() []error {
	out := make([]error, len(e.Failures))
	for i, f := range e.Failures {
		out[i] = f
	}
	return out
}
func (e *OperationOutcomesError) LastCause() any {
	if len(e.Failures) == 0 {
		return nil
	}
	return e.Failures[len(e.Failures)-1].Cause
}
func failureFromOutcomes(receipts []dispatch.Receipt) error {
	out := &OperationOutcomesError{}
	for _, receipt := range receipts {
		if failure := dispatch.OutcomeError(receipt); failure != nil {
			out.Failures = append(out.Failures, failure)
		}
	}
	if len(out.Failures) == 0 {
		return nil
	}
	return out
}
