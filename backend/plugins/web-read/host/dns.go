//go:build web_read_candidate

package host

import (
	"context"
	"errors"
	"net/netip"
	"sync"
)

var ErrOSDNSUnavailable = errors.New("web_read_os_dns_unavailable")
var ErrOSDNSClosed = errors.New("web_read_os_dns_closed")

// OSResolver owns synchronous OS lookups. Construct it with NewOSResolver;
// its zero value cannot resolve. It is not safe to copy after first use.
// This port takes the already URL-normalized ASCII hostname from networkPrepare,
// not arbitrary Unicode DNS names. It implements the source's default verbatim
// result order, not Node process-wide DNS order overrides.
type OSResolver struct {
	mu      sync.Mutex
	closed  bool
	workers sync.WaitGroup
	lookup  func(string) DNSResult
}

// NewOSResolver returns an explicit error on platforms without the native port.
// It does not register a manager service or change Network's default ports.
func NewOSResolver() (*OSResolver, error) {
	if !osDNSAvailable {
		return nil, ErrOSDNSUnavailable
	}
	return &OSResolver{lookup: lookupOS}, nil
}

// Lookup admits a native call unless already cancelled or closed. Once admitted,
// cancellation belongs to Network's logical deadline; it cannot cancel libc.
// Result is a single buffered publication, so abandoning it never holds native
// resources. Drained closes only after the synchronous lookup AND its cleanup
// return. Neither a timer nor context cancellation can close Drained.
func (r *OSResolver) Lookup(ctx context.Context, hostname string) LookupJob {
	result := make(chan DNSResult, 1)
	drained := make(chan struct{})
	job := LookupJob{Result: result, Drained: drained}
	r.mu.Lock()
	err := ctx.Err()
	if err == nil && r.closed {
		err = ErrOSDNSClosed
	}
	if err == nil && r.lookup == nil {
		err = ErrOSDNSUnavailable
	}
	if err != nil {
		r.mu.Unlock()
		result <- DNSResult{Err: err}
		close(result)
		close(drained)
		return job
	}
	r.workers.Add(1)
	lookup := r.lookup
	r.mu.Unlock()
	go func() {
		defer r.workers.Done()
		defer close(drained)
		defer close(result)
		result <- lookup(hostname)
	}()
	return job
}

// Close prevents new admissions, without claiming existing native calls ended.
// A generation owner must stop logical requests and then call CloseAndWait (or
// Close followed by Wait) before reporting physical shutdown complete.
func (r *OSResolver) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

// Wait first closes admission, then joins every admitted native lookup. It may
// block indefinitely if the OS resolver does. There is intentionally no timeout
// that reports a false drain; process termination is not graceful completion.
func (r *OSResolver) Wait()         { r.Close(); r.workers.Wait() }
func (r *OSResolver) CloseAndWait() { r.Wait() }

// Native results keep family, duplicates, and order. In particular IPv4-mapped
// IPv6 must remain IPv6, as in Node's AfterGetAddrInfo.
func nativeDNSAddress(raw []byte) (netip.Addr, bool) { return netip.AddrFromSlice(raw) }
