//go:build !linux || !amd64 || !cgo

package publicpage

import (
	"context"
	"net"

	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
)

var resolver = &net.Resolver{PreferGo: true}

func NativeLookup(ctx context.Context, hostname string) pinnedhop.LookupJob {
	result, drained := make(chan pinnedhop.DNSResult, 1), make(chan struct{})
	go func() {
		addresses, err := resolver.LookupNetIP(ctx, "ip", hostname)
		result <- pinnedhop.DNSResult{Addresses: addresses, Err: err}
		close(drained)
	}()
	return pinnedhop.LookupJob{Result: result, Drained: drained}
}
