package assertions

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/identityprivacy"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// PersonPresentation reads the scene-local nickname data, not identity links.
func (r *Reader) PersonPresentation(scene, person string) (any, error) {
	var raw string
	found, err := r.one("SELECT value FROM config WHERE key=?", []any{"identity:" + scene + ":" + person}, &raw)
	if err != nil || !found {
		return nil, err
	}
	return jsjson.Parse(raw)
}

// IdentityAdministration borrows this snapshot and returns only the closed
// registry projection. It never opens or creates a registry.
func (r *Reader) IdentityAdministration() (*Object, error) {
	return identityprivacy.ReadAdminRegistry(r.AssertReady, func(q string, args ...any) (sourceevents.Rows, error) {
		return r.query(q, args...)
	})
}
