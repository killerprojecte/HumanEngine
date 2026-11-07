//go:build wasip1

// Package abi owns guest memory for the V2 JSON call ABI.
package abi

import (
	"encoding/json"
	"unsafe"
)

var buffers = map[uint32][]byte{}

func Alloc(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	b := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	buffers[ptr] = b
	return ptr
}
func Free(ptr uint32) { delete(buffers, ptr) }
func Invoke(ptr, size uint32, handler func([]byte) any) uint64 {
	input := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	output, err := json.Marshal(handler(input))
	if err != nil {
		output = []byte(`{"error":"plugin_output_encoding_failed"}`)
	}
	out := Alloc(uint32(len(output)))
	copy(buffers[out], output)
	return uint64(out)<<32 | uint64(len(output))
}
