package pluginmanager

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/publicip"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"golang.org/x/net/http/httpguts"
)

type structuredEndpoint string

// The endpoint is fixed at local package admission, never picked by a tool
// argument or guest result. Auth is delivered only to this TLS origin.
func parseStructuredEndpoint(raw string) (structuredEndpoint, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !httpguts.ValidHostHeader(u.Host) ||
		u.Hostname() == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") ||
		strings.ContainsAny(raw, "?#") {
		return "", errors.New("external_v2_structured_endpoint_invalid")
	}
	return structuredEndpoint(u.String()), nil
}

var structuredResolver = &net.Resolver{PreferGo: true}

type structuredTransport struct {
	endpoint structuredEndpoint
	auth     string
	life     context.Context
	pool     *http.Transport
	dials    quiescence.Group
}

func newStructuredTransport(life context.Context, endpoint structuredEndpoint, key string) *structuredTransport {
	s := &structuredTransport{endpoint: endpoint, auth: "Bearer " + key, life: life}
	s.pool = &http.Transport{
		DialContext: s.dial, ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
	}
	return s
}

func (s *structuredTransport) dial(ctx context.Context, network, address string) (net.Conn, error) {
	done, ok := s.dials.Start()
	if !ok {
		return nil, context.Canceled
	}
	defer done()
	// Transport may carry out a detached dial after its request is abandoned.
	// The generation owns the native lookup, as well as its own deadline.
	dial, cancel := context.WithTimeout(ctx, 5*time.Second)
	stop := context.AfterFunc(s.life, cancel)
	defer stop()
	defer cancel()
	if err := s.life.Err(); err != nil {
		return nil, err
	}
	hostname, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := structuredResolver.LookupNetIP(dial, "ip", hostname)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !publicip.Allowed(ip.Unmap()) {
			return nil, errors.New("structured_address_not_public")
		}
	}
	dialer := net.Dialer{}
	for _, ip := range ips {
		conn, err := dialer.DialContext(dial, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("structured_dial_failed")
}

func (s *structuredTransport) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, string(s.endpoint), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// A consumed, possibly charged POST is not replayable.
	req.GetBody = nil
	req.Header.Set("Authorization", s.auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// RoundTrip never follows a redirect. The pool's standard gzip handling
	// hands the cache the decoded entity, matching the TypeScript result.
	return s.pool.RoundTrip(req)
}

func (s *structuredTransport) Dispose() {
	s.dials.Seal()
	s.pool.CloseIdleConnections()
}

func (s *structuredTransport) CloseAndWait(ctx context.Context) error {
	s.Dispose()
	return s.dials.Wait(ctx)
}
