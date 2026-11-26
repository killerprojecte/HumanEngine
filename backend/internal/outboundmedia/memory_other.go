//go:build !linux

package outboundmedia

// Non-Linux libuv parity is not implemented. An unknown host budget must not
// silently become unlimited; callers must inject a verified platform probe.
func availableMemory() uint64 { return 0 }
