package timezones

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

// Resolve accepts exactly the source Intl timezone names and numeric offsets.
// Named identifiers are canonicalized from the pinned Node alias census.
func Resolve(value string) (canonical string, offsetMinutes int, fixed bool, ok bool) {
	if match := offsetZone.FindStringSubmatch(value); match != nil {
		hours, _ := strconv.Atoi(match[2])
		minutes, _ := strconv.Atoi(match[3])
		if hours < 24 && minutes < 60 {
			offset := hours*60 + minutes
			if match[1] != "+" {
				offset = -offset
			}
			return value, offset, true, true
		}
	}
	for _, r := range value {
		if r > 127 {
			return "", 0, false, false
		}
	}
	name, ok := timezoneNames[strings.ToLower(value)]
	return name, 0, name == "UTC", ok
}
