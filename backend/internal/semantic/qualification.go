package semantic

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// ValidateAnchorQualification shares the existing source schema and field order.
func ValidateAnchorQualification(v *jsjson.Object) (*jsjson.Object, error) {
	out, err := scQualification(v)
	if err != nil {
		return nil, err
	}
	return scObject(out), nil
}
