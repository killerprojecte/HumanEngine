//go:build wasip1

// Package abi owns guest memory for the V2 JSON call ABI.
package abi

import (
	"encoding/json"
	"unsafe"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

var buffers = map[uint32][]byte{}

func Alloc(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	b := make([]byte, size)
	return retain(b)
}

func retain(b []byte) uint32 {
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	buffers[ptr] = b
	return ptr
}
func Free(ptr uint32) { delete(buffers, ptr) }
func Invoke(ptr, size uint32, handler func([]byte) any) uint64 {
	input := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	value := handler(input)
	var output []byte
	var err error
	// Serialize typed JS objects once, preserving their UTF-16 semantics.
	if object, ok := value.(*jsjson.Object); ok {
		output, err = jsjson.Marshal(object)
	} else {
		output, err = json.Marshal(value)
	}
	if err != nil {
		output = []byte(`{"error":"plugin_output_encoding_failed"}`)
	}
	// Both encoders return owned bytes; keep them alive until the host frees them.
	out := retain(output)
	return uint64(out)<<32 | uint64(len(output))
}
