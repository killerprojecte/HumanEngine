package assertions

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// PersonPresentation reads the scene-local nickname data, not identity links.
func (r *Reader) PersonPresentation(scene, person string) (any, error) {
	var raw string
	found, err := r.one("SELECT value FROM config WHERE key=?", []any{"identity:" + scene + ":" + person}, &raw)
	if err != nil || !found {
		return nil, err
	}
	return jsjson.Parse(raw)
}
