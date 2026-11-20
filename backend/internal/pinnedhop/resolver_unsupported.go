//go:build !cgo || ((!linux || !amd64) && (!web_read_candidate || (!linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly)))

package pinnedhop

const osDNSAvailable = false

func lookupOS(string) DNSResult { return DNSResult{Err: ErrOSDNSUnavailable} }
