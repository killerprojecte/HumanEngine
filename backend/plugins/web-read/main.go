//go:build wasip1

package main

import (
	"github.com/killerprojecte/HumanEngine/backend/plugins/abi"
	"github.com/killerprojecte/HumanEngine/backend/plugins/webread"
)

//go:wasmexport alloc
func alloc(size uint32) uint32 { return abi.Alloc(size) }

//go:wasmexport free
func free(ptr uint32) { abi.Free(ptr) }

//go:wasmexport plugin_call
func call(ptr, size uint32) uint64 { return abi.Invoke(ptr, size, webread.Invoke) }
func main()                        {}
