package settingsstore

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// ParseBudgetSettings exposes the existing source-pinned schema to the native
// budget service. Unlike the settings-write security projection, explicit null
// is invalid at this read boundary.
func ParseBudgetSettings(input any) (*jsjson.Object, error) {
	if input == nil {
		return nil, &BudgetValidationError{[]any{invalidType("object", "", input)}}
	}
	return budgetProjection(input)
}
