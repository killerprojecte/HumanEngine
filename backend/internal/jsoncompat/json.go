// Package jsoncompat contains byte-level compatibility rules shared by persisted formats.
package jsoncompat

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

// StringArray reproduces JSON.stringify for arrays of valid Unicode strings.
// encoding/json escapes HTML and U+2028/U+2029, which would change legacy AAD.
func StringArray(values ...string) []byte {
	var out bytes.Buffer
	out.WriteByte('[')
	for i, value := range values {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteByte('"')
		for _, r := range value {
			switch r {
			case '"', '\\':
				out.WriteByte('\\')
				out.WriteRune(r)
			case '\b':
				out.WriteString(`\b`)
			case '\f':
				out.WriteString(`\f`)
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				if r < 0x20 {
					fmt.Fprintf(&out, `\u%04x`, r)
				} else if utf8.ValidRune(r) {
					out.WriteRune(r)
				} else {
					out.WriteRune(utf8.RuneError)
				}
			}
		}
		out.WriteByte('"')
	}
	out.WriteByte(']')
	return out.Bytes()
}
