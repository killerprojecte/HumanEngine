package assertions

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
)

func Fingerprint(record *Object) (string, error) {
	normalized, e := jsstring.FingerprintText(str(get(record, "content")))
	if e != nil {
		return "", e
	}
	var person any = ""
	sc := get(record, "scope")
	if get(sc, "kind") == "scene" {
		if p := get(sc, "personId"); p != nil {
			person = p
		}
	}
	raw, e := jsjson.Stringify([]any{scope(record), person, normalized})
	if e != nil {
		return "", e
	}
	return digest(raw), nil
}
func HistoricalFingerprint(record *Object) (string, error) {
	hash, e := Fingerprint(record)
	if e != nil {
		return "", e
	}
	return "history:" + hash + ":" + str(get(record, "id")), nil
}
