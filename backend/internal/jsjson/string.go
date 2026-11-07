package jsjson

import (
	"errors"
	"unicode/utf8"
)

// ErrStringEncoding means a Go string is not canonical WTF-8. Canonical WTF-8
// is UTF-8 extended with three-byte encodings of unpaired UTF-16 surrogates.
// A paired high/low surrogate must use the ordinary four-byte UTF-8 encoding.
var ErrStringEncoding = errors.New("jsjson: string is not canonical WTF-8")

// FromUTF16 returns the canonical, lossless Go representation of a JS string.
// Do not pass its result through encoding/json, rune conversion, or other
// Unicode-scalar-only APIs: those APIs replace unpaired surrogates with U+FFFD.
func FromUTF16(units []uint16) string {
	var b []byte
	for i := 0; i < len(units); i++ {
		r := rune(units[i])
		if high(r) && i+1 < len(units) && low(rune(units[i+1])) {
			r = 0x10000 + (r-0xd800)*0x400 + rune(units[i+1]) - 0xdc00
			i++
		}
		b = appendRune(b, r)
	}
	return string(b)
}

// UTF16 returns exactly the code units used by a JS string.
func UTF16(s string) ([]uint16, error) {
	if err := ValidateString(s); err != nil {
		return nil, err
	}
	units := make([]uint16, 0)
	for i := 0; i < len(s); {
		r, n := decodeRune(s[i:])
		i += n
		if r <= 0xffff {
			units = append(units, uint16(r))
		} else {
			r -= 0x10000
			units = append(units, uint16(0xd800+r/0x400), uint16(0xdc00+r%0x400))
		}
	}
	return units, nil
}

// UTF16Len is JavaScript string.length, not the number of Unicode code points.
func UTF16Len(s string) (int, error) {
	return stringLength(s, true)
}

// CodePointCount is [...string].length in JavaScript. An unpaired surrogate
// counts as one, and a valid surrogate pair counts as one rather than two.
func CodePointCount(s string) (int, error) {
	return stringLength(s, false)
}

func stringLength(s string, codeUnits bool) (int, error) {
	if err := ValidateString(s); err != nil {
		return 0, err
	}
	count := 0
	for i := 0; i < len(s); {
		r, n := decodeRune(s[i:])
		i += n
		count++
		if codeUnits && r > 0xffff {
			count++
		}
	}
	return count, nil
}

// ValidateString rejects invalid UTF-8 other than lone surrogate encodings,
// and rejects adjacent encoded high/low surrogates (a noncanonical pair).
// This ensures Go equality and map keys agree with JS string equality.
func ValidateString(s string) error {
	previousHigh := false
	for i := 0; i < len(s); {
		r, n := decodeRune(s[i:])
		if n == 0 || (previousHigh && low(r)) {
			return ErrStringEncoding
		}
		previousHigh = high(r)
		i += n
	}
	return nil
}

func high(r rune) bool { return r >= 0xd800 && r <= 0xdbff }
func low(r rune) bool  { return r >= 0xdc00 && r <= 0xdfff }

// decodeRune extends UTF-8 decoding by admitting surrogate code points.
// A zero width denotes malformed input, not a replacement character.
func decodeRune(s string) (rune, int) {
	if len(s) >= 3 && s[0] == 0xed && s[1] >= 0xa0 && s[1] <= 0xbf && s[2]&0xc0 == 0x80 {
		return rune(s[0]&0x0f)<<12 | rune(s[1]&0x3f)<<6 | rune(s[2]&0x3f), 3
	}
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && n == 1 {
		return 0, 0
	}
	return r, n
}

func appendRune(b []byte, r rune) []byte {
	if high(r) || low(r) {
		return append(b, 0xe0|byte(r>>12), 0x80|byte(r>>6&0x3f), 0x80|byte(r&0x3f))
	}
	return utf8.AppendRune(b, r)
}

// UTF8Text applies JavaScript's UTF-8 text encoding boundary (Buffer.from and
// Node SQLite string bindings): lone surrogates become U+FFFD. This is deliberately
// separate from Stringify, which must preserve those code units as JSON escapes.
// Never use this conversion before hashing JSON.stringify output or comparing
// JavaScript identities. Embedded NUL bytes are retained.
func UTF8Text(s string) (string, error) {
	if err := ValidateString(s); err != nil {
		return "", err
	}
	var b []byte
	for i := 0; i < len(s); {
		r, n := decodeRune(s[i:])
		i += n
		if high(r) || low(r) {
			r = '\ufffd'
		}
		b = appendRune(b, r)
	}
	return string(b), nil
}
