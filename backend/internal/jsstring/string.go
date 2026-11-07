// Package jsstring implements the JavaScript string operations used in memory
// fingerprints, pinned to Node 24.22.1 and Unicode 17.0. It accepts the canonical
// WTF-8 representation documented by jsjson, preserving lone UTF-16 surrogates.
package jsstring

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

type codePointRange struct {
	first, last rune
}

type lowercaseMapping struct {
	point rune
	units []uint16
}

// FingerprintText is content.trim().replace(/\s+/g, ' ').toLowerCase(). It does
// not normalize Unicode, hash, or serialize the result. Use jsjson.Stringify
// rather than encoding/json when constructing the fingerprint's JSON array.
// Malformed or noncanonical WTF-8 returns jsjson.ErrStringEncoding.
func FingerprintText(value string) (string, error) {
	points, err := codePoints(value)
	if err != nil {
		return "", err
	}
	// Collapse in place. Delaying the space removes both leading and trailing
	// ECMAScript whitespace without treating U+0085 as whitespace.
	collapsed := points[:0]
	space := false
	for _, point := range points {
		if inRanges(point, whitespaceRanges[:]) {
			space = len(collapsed) != 0
			continue
		}
		if space {
			collapsed = append(collapsed, ' ')
			space = false
		}
		collapsed = append(collapsed, point)
	}
	return lowerPoints(collapsed), nil
}

// Lower is the locale-independent JavaScript String.prototype.toLowerCase.
// Its tables are generated from the pinned Node runtime, not Go's Unicode
// tables. Context is evaluated on the original input, before any expansion.
func Lower(value string) (string, error) {
	points, err := codePoints(value)
	if err != nil {
		return "", err
	}
	return lowerPoints(points), nil
}

func codePoints(value string) ([]rune, error) {
	units, err := jsjson.UTF16(value)
	if err != nil {
		return nil, err
	}
	points := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		point := rune(units[i])
		if point >= 0xd800 && point <= 0xdbff && i+1 < len(units) && units[i+1] >= 0xdc00 && units[i+1] <= 0xdfff {
			point = 0x10000 + (point-0xd800)*0x400 + rune(units[i+1]) - 0xdc00
			i++
		}
		points = append(points, point)
	}
	return points, nil
}

func lowerPoints(points []rune) string {
	units := make([]uint16, 0, len(points))
	precededByCased := false
	for i, point := range points {
		if point == 0x03a3 && precededByCased && !followedByCased(points[i+1:]) {
			units = append(units, 0x03c2)
		} else if mapping := lowercase(point); mapping != nil {
			units = append(units, mapping...)
		} else {
			units = appendPoint(units, point)
		}
		// Case_Ignorable wins when a character is also Cased (e.g. U+0345).
		if !inRanges(point, caseIgnorableRanges[:]) {
			precededByCased = inRanges(point, casedRanges[:])
		}
	}
	return jsjson.FromUTF16(units)
}

func followedByCased(points []rune) bool {
	// Sigma is itself non-ignorable, so runs examined for different sigmas
	// cannot overlap. These lookaheads together are linear, not quadratic.
	for _, point := range points {
		if !inRanges(point, caseIgnorableRanges[:]) {
			return inRanges(point, casedRanges[:])
		}
	}
	return false
}

func appendPoint(units []uint16, point rune) []uint16 {
	if point <= 0xffff {
		return append(units, uint16(point))
	}
	point -= 0x10000
	return append(units, uint16(0xd800+point/0x400), uint16(0xdc00+point%0x400))
}

func lowercase(point rune) []uint16 {
	lo, hi := 0, len(lowercaseMappings)
	for lo < hi {
		mid := lo + (hi-lo)/2
		mapping := &lowercaseMappings[mid]
		if point < mapping.point {
			hi = mid
		} else if point > mapping.point {
			lo = mid + 1
		} else {
			return mapping.units
		}
	}
	return nil
}

func inRanges(point rune, ranges []codePointRange) bool {
	lo, hi := 0, len(ranges)
	for lo < hi {
		mid := lo + (hi-lo)/2
		entry := ranges[mid]
		if point < entry.first {
			hi = mid
		} else if point > entry.last {
			lo = mid + 1
		} else {
			return true
		}
	}
	return false
}
