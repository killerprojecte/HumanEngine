//go:build cgo

package pinnedhop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"strings"
)

const resolverChildArg = "--internal-media-resolver"

// RunResolverChild enters the private one-shot child before server startup.
// Only the exact invocation is handled. The child owns no application state.
func RunResolverChild(args []string) (bool, error) {
	if len(args) != 1 || args[0] != resolverChildArg {
		return false, nil
	}
	return true, serveResolverChild(os.Stdin, os.Stdout)
}

func serveResolverChild(in io.Reader, out io.Writer) error {
	// libuv's hostname buffer holds at most 255 ASCII payload bytes + NUL.
	host, err := io.ReadAll(io.LimitReader(in, 256))
	if err != nil || len(host) == 0 || len(host) >= 256 {
		return errors.New("invalid_resolver_request")
	}
	for _, c := range host {
		if c == 0 || c >= 128 {
			return errors.New("invalid_resolver_request")
		}
	}
	result := lookupOS(string(host))
	if result.Err != nil {
		return errors.New("resolver_lookup_failed")
	}
	// Ordered fixed-width per-family address records; do not unmap IPv6.
	var response []byte
	for _, addr := range result.Addresses {
		if addr.Is4() {
			a := addr.As4()
			response = append(response, 4)
			response = append(response, a[:]...)
		} else {
			a := addr.As16()
			response = append(response, 6)
			response = append(response, a[:]...)
		}
	}
	_, err = out.Write(response)
	return err
}

// LookupChild starts one owned process for a DNS name; the caller owns the
// lookup deadline and keeps its physical lease until Drained closes. Only the
// exact executing Linux binary is eligible, with no PATH or shell resolution.
func LookupChild(ctx context.Context, hostname string) LookupJob {
	return lookupChild(ctx, "/proc/self/exe", hostname)
}

func lookupChild(ctx context.Context, executable, hostname string) LookupJob {
	result := make(chan DNSResult, 1)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		defer close(result)
		result <- lookupChildSync(ctx, executable, hostname)
	}()
	return LookupJob{Result: result, Drained: drained}
}

func lookupChildSync(ctx context.Context, executable, hostname string) DNSResult {
	if err := ctx.Err(); err != nil {
		return DNSResult{Err: err}
	}
	cmd := exec.CommandContext(ctx, executable, resolverChildArg)
	cmd.Stdin = strings.NewReader(hostname)
	var response bytes.Buffer
	cmd.Stdout = &response
	// Kill on actual cancellation only. Run includes Wait/reap and completion
	// of both IO copy paths; a stuck kernel task is not reported as drained.
	if err := cmd.Run(); err != nil {
		if err := ctx.Err(); err != nil {
			return DNSResult{Err: err}
		}
		return DNSResult{Err: errors.New("resolver_child_failed")}
	}
	if err := ctx.Err(); err != nil {
		return DNSResult{Err: err}
	}
	var addresses []netip.Addr
	raw := response.Bytes()
	for len(raw) > 0 {
		size := 0
		switch raw[0] {
		case 4:
			size = 4
		case 6:
			size = 16
		}
		if size == 0 || len(raw) <= size {
			return DNSResult{Err: errors.New("invalid_resolver_response")}
		}
		addr, _ := netip.AddrFromSlice(raw[1 : 1+size])
		addresses = append(addresses, addr)
		raw = raw[1+size:]
	}
	if len(addresses) == 0 {
		return DNSResult{Err: errors.New("invalid_resolver_response")}
	}
	return DNSResult{Addresses: addresses}
}
