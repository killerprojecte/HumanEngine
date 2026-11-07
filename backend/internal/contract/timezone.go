package contract

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Oracle: Node v24.22.1, ICU 78.3, tzdata 2026e.
//
//go:embed timezone-names-node.json
var timezoneNamesJSON []byte
var timezoneNames = func() map[string]string {
	var names map[string]string
	if err := json.Unmarshal(timezoneNamesJSON, &names); err != nil {
		panic(err)
	}
	return names
}()
var offsetZone = regexp.MustCompile(`^([+−-])([0-9]{2})(?::?([0-9]{2}))?$`)

// validTimeZone preserves Node 24 Intl's case-insensitive named zones and explicit
// numeric offsets; it does not accept Go-only empty/Local conveniences.
func validTimeZone(value string) bool {
	if match := offsetZone.FindStringSubmatch(value); match != nil {
		hours, _ := strconv.Atoi(match[2])
		minutes, _ := strconv.Atoi(match[3])
		if hours < 24 && minutes < 60 {
			return true
		}
	}
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	_, ok := timezoneNames[strings.ToLower(value)]
	if !ok {
		return false
	}
	return true
}
