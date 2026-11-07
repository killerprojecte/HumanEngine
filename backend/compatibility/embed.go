// Package compatibility embeds the frozen contract; it is independent of the legacy runtime.
package compatibility

import _ "embed"

//go:embed openapi.json
var OpenAPI []byte
