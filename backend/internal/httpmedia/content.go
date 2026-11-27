// Package httpmedia identifies media types shown as UTF-8 tool previews.
package httpmedia

import "strings"

func Textual(contentType string) bool {
	mime, _, _ := strings.Cut(contentType, ";")
	mime = strings.ToLower(strings.TrimSpace(mime))
	return strings.HasPrefix(mime, "text/") || strings.HasSuffix(mime, "json") ||
		strings.HasSuffix(mime, "xml") || strings.HasSuffix(mime, "javascript")
}
