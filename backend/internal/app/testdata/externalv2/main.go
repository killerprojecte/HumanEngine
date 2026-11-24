//go:build wasip1

package main

import (
	"encoding/json"

	"github.com/killerprojecte/HumanEngine/backend/plugins/abi"
)

func main() {}

//go:wasmexport alloc
func alloc(size uint32) uint32 { return abi.Alloc(size) }

//go:wasmexport free
func free(ptr uint32) { abi.Free(ptr) }

//go:wasmexport plugin_call
func pluginCall(ptr, size uint32) uint64 { return abi.Invoke(ptr, size, call) }

var reads int
var marker = "external read fixture"

func call(raw []byte) any {
	var request struct {
		Operation string
		Tool      string
		Input     struct{ Key string }
	}
	if json.Unmarshal(raw, &request) != nil {
		return map[string]string{"error": "invalid_request"}
	}
	switch request.Operation {
	case "registrations":
		return []any{map[string]any{
			"name": "stationary-fixture.read", "description": "Read an independent fixture.",
			"effect": "read", "inputSchema": map[string]any{"type": "object",
				"properties": map[string]any{"key": map[string]string{"type": "string"}},
				"required":   []string{"key"}, "additionalProperties": false},
		}}
	case "toolExecute":
		if request.Tool == "stationary-fixture.read" && request.Input.Key == "sample" {
			reads++
			return map[string]any{"marker": marker, "reads": reads}
		}
	}
	return map[string]string{"error": "unexpected_request"}
}
