package httpapi

import (
	"net/url"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

func memoryPageQuery(values url.Values) *memoryservice.AdminPageQuery {
	valid := true
	first := func(key, fallback string) string {
		if v, ok := values[key]; ok {
			valid = valid && len(v) == 1
			return v[0]
		}
		return fallback
	}
	page, okPage := coerceQueryInteger(first("page", "1"), 9007199254740991)
	limit, okLimit := coerceQueryInteger(first("limit", "20"), 100)
	scene := first("scene", "all")
	kind := first("kind", "all")
	status := first("status", "all")
	locale := first("locale", "en")
	person, search := first("person", ""), first("q", "")
	if !valid || !okPage || !okLimit || (scene != "all" && scene != "public" && (!strings.HasPrefix(scene, "scene:") || len(scene) == 6)) ||
		!experienceMember(kind, "all", "fact", "claim", "term", "experience", "summary") ||
		!experienceMember(status, "all", "active", "disputed", "superseded", "invalidated") ||
		len(locale) > 128 {
		return nil
	}
	return &memoryservice.AdminPageQuery{
		Page: page, Limit: int(limit), Scene: scene, Kind: kind, Status: status,
		Person: person, Q: search, Locale: locale,
	}
}
