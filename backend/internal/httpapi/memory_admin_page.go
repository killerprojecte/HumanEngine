package httpapi

import (
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
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
	integer := func(key, fallback string, maximum float64) (int64, bool) {
		text := jsstring.Trim(first(key, fallback))
		var n float64
		var err error
		if len(text) > 2 && text[0] == '0' && strings.ContainsAny(text[1:2], "xXoObB") {
			if strings.ContainsRune(text, '_') {
				return 0, false
			}
			var u uint64
			u, err = strconv.ParseUint(text, 0, 64)
			n = float64(u)
		} else {
			if !experienceDecimal.MatchString(text) {
				return 0, false
			}
			n, err = strconv.ParseFloat(text, 64)
		}
		if err != nil || n < 1 || n > maximum || math.Trunc(n) != n {
			return 0, false
		}
		return int64(n), true
	}
	page, okPage := integer("page", "1", 9007199254740991)
	limit, okLimit := integer("limit", "20", 100)
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
