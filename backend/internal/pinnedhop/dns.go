// Package pinnedhop supplies joinable DNS and literal-address TCP/TLS
// primitives. The caller owns destination admission and HTTP policy.
package pinnedhop

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

type DNSResult struct {
	Addresses []netip.Addr
	Err       error
}
type LookupJob struct {
	Result  <-chan DNSResult
	Drained <-chan struct{}
}
type Lookup func(context.Context, string) LookupJob

// Resolve owns DNS settlement separately from draining native work.
func Resolve(ctx context.Context, hostname string, timeout time.Duration, lookup Lookup, beginWork func() (func(), bool)) ([]netip.Addr, error) {
	if lookup == nil {
		return nil, errors.New("web_read_joinable_dns_port_missing")
	}
	dns, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done, ok := beginWork()
	if !ok {
		return nil, context.Canceled
	}
	job := lookup(dns, hostname)
	if job.Result == nil || job.Drained == nil {
		done()
		return nil, errors.New("web_read_invalid_dns_job")
	}
	// One relay owns the resolver result channel until physical completion.
	// It keeps receiving even after the logical timeout, including from an
	// unbuffered producer that can close Drained only after its send.
	settled := make(chan DNSResult, 1)
	go func() {
		defer done()
		results := job.Result
		published := false
		for {
			select {
			case result, open := <-results:
				if !open {
					results = nil
					continue
				}
				if !published {
					settled <- result
					published = true
				}
			case <-job.Drained:
				// A buffered result may already be available when Drained closes.
				// Consume it before retiring; never race success into a timeout.
				for {
					select {
					case result, open := <-results:
						if !open {
							return
						}
						if !published {
							settled <- result
							published = true
						}
					default:
						return
					}
				}
			}
		}
	}()
	var found DNSResult
	select {
	case <-dns.Done():
		if ctx.Err() != nil {
			return nil, errors.New("media_cancelled")
		}
		return nil, errors.New("media_dns_timeout")
	case found = <-settled:
	}
	// In particular, late success MUST NOT bypass the independent timer.
	if ctx.Err() != nil {
		return nil, errors.New("media_cancelled")
	}
	if dns.Err() != nil {
		return nil, errors.New("media_dns_timeout")
	}
	if found.Err != nil {
		return nil, errors.New("media_dns_failed")
	}
	return found.Addresses, nil
}
