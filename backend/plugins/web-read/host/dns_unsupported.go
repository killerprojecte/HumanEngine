//go:build web_read_candidate && (!cgo || (!linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly))

package host

const osDNSAvailable = false

func lookupOS(string) DNSResult { return DNSResult{Err: ErrOSDNSUnavailable} }
