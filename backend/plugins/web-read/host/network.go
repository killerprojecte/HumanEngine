//go:build web_read_candidate

package host

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// ErrHTTP101Pending marks the isolated, unapproved protocol branch. This
// candidate must NOT be registered as a production builtin until that branch is
// resolved against the source and the user's pending decision. It is not a new
// user-visible web-read status or an endorsed replacement for source behavior.
var ErrHTTP101Pending = errors.New("web_read_http101_pending_decision")

// Lookup separates source-visible settlement from actual resolver completion.
// There is deliberately no DefaultResolver adapter: its singleflight/cgo
// internals do not expose a joinable completion boundary.
type DNSResult struct {
	Addresses []netip.Addr
	Err       error
}
type LookupJob struct {
	Result  <-chan DNSResult
	Drained <-chan struct{}
}
type Lookup func(context.Context, string) LookupJob

// Network supplies only DNS/TCP/TLS/stream facilities. Its guest binding must be
// the same generation as the Service. No environment proxy, cookie jar, content
// decompression or automatic redirect client is used.
type Network struct {
	BeginWork              func() (func(), bool)
	Life                   context.Context
	CallGuestWithAdmission func(context.Context, context.Context, []byte) ([]byte, error)
	Lookup                 Lookup
	DialContext            func(context.Context, string, string) (net.Conn, error)
	TLSConfig              *tls.Config // explicit injection for synthetic TLS fixtures only
}
type wireAddress struct {
	Address string `json:"address"`
	Family  int    `json:"family"`
}
type wireState struct {
	URL         string            `json:"url"`
	Method      string            `json:"method"`
	Headers     map[string]string `json:"headers"`
	HeaderOrder []string          `json:"headerOrder"`
	Body        *string           `json:"body,omitempty"`
}
type hop struct {
	WireHeaders  [][2]string     `json:"wireHeaders"`
	State        json.RawMessage `json:"state"`
	Hostname     string          `json:"hostname"`
	Port         string          `json:"port"`
	TLS          bool            `json:"tls"`
	Host         string          `json:"host"`
	Path         string          `json:"path"`
	DNSTimeoutMS float64         `json:"dnsTimeoutMs"`
	Literal      *wireAddress    `json:"literal"`
	Address      wireAddress     `json:"address"`
	Response     bool            `json:"response"`
}
type policyFailure struct {
	message   string
	typeError bool
}

func (e policyFailure) Error() string   { return e.message }
func (e policyFailure) TypeError() bool { return e.typeError }
func (n Network) guest(ctx context.Context, operation string, in map[string]any, out any) error {
	in["operation"] = operation
	data, e := json.Marshal(in)
	if e != nil {
		return e
	}
	raw, e := admittedGuest(ctx, n.Life, n.BeginWork, n.CallGuestWithAdmission, data)
	if e != nil {
		return e
	}
	var failure struct {
		Error     string `json:"error"`
		TypeError bool   `json:"typeError"`
	}
	if e = json.Unmarshal(raw, &failure); e != nil {
		return e
	}
	if failure.Error != "" {
		return policyFailure{failure.Error, failure.TypeError}
	}
	return json.Unmarshal(raw, out)
}
func (n Network) Fetch(ctx context.Context, request json.RawMessage, timeout time.Duration) (Resource, error) {
	if n.CallGuestWithAdmission == nil || n.Life == nil || n.BeginWork == nil {
		return Resource{}, errors.New("web_read_network_ports_missing")
	}
	// Source requestPublicResource owns a second absolute timeout, independent of
	// the service's timer. Keep it alive through final response consumption.
	life, cancel := context.WithTimeout(ctx, timeout)
	stopLife := context.AfterFunc(n.Life, cancel)
	if n.Life.Err() != nil {
		cancel()
	}
	finishLifetime := func() { stopLife(); cancel() }
	fail := func(e error) (Resource, error) { finishLifetime(); return Resource{}, e }
	var next hop
	if e := n.guest(life, "networkPrepare", map[string]any{"request": request, "timeoutMs": float64(timeout / time.Millisecond)}, &next); e != nil {
		return fail(e)
	}
	for {
		address, e := n.resolve(life, next)
		if e != nil {
			return fail(e)
		}
		if life.Err() != nil {
			return fail(errors.New("media_cancelled"))
		}
		response, e := n.open(life, next, address)
		if e != nil {
			return fail(e)
		}
		var decision hop
		e = n.guest(life, "networkRedirect", map[string]any{"state": next.State, "httpStatus": response.StatusCode, "headers": response.Headers}, &decision)
		if e != nil {
			response.Finish()
			return fail(e)
		}
		if !decision.Response {
			response.Finish()
			next = decision
			continue
		}
		if life.Err() != nil {
			response.Finish()
			return fail(errors.New("network_cancelled"))
		}
		finish := response.Finish
		response.Finish = func() { finish(); finishLifetime() }
		return response, nil
	}
}
func (n Network) resolve(ctx context.Context, h hop) (wireAddress, error) {
	var addresses []wireAddress
	if h.Literal != nil {
		addresses = []wireAddress{*h.Literal}
	} else {
		if n.Lookup == nil {
			return wireAddress{}, errors.New("web_read_joinable_dns_port_missing")
		}
		dns, cancel := context.WithTimeout(ctx, time.Duration(h.DNSTimeoutMS)*time.Millisecond)
		defer cancel()
		done, ok := n.BeginWork()
		if !ok {
			return wireAddress{}, context.Canceled
		}
		job := n.Lookup(dns, h.Hostname)
		if job.Result == nil || job.Drained == nil {
			done()
			return wireAddress{}, errors.New("web_read_invalid_dns_job")
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
				return wireAddress{}, errors.New("media_cancelled")
			}
			return wireAddress{}, errors.New("media_dns_timeout")
		case found = <-settled:
		}
		// In particular, late success MUST NOT bypass the independent timer.
		if ctx.Err() != nil {
			return wireAddress{}, errors.New("media_cancelled")
		}
		if dns.Err() != nil {
			return wireAddress{}, errors.New("media_dns_timeout")
		}
		if found.Err != nil {
			return wireAddress{}, errors.New("media_dns_failed")
		}
		ips := found.Addresses

		for _, ip := range ips {
			family := 6
			if ip.Is4() {
				family = 4
			}
			addresses = append(addresses, wireAddress{ip.String(), family})
		}
	}
	if ctx.Err() != nil {
		return wireAddress{}, errors.New("media_cancelled")
	}
	var chosen hop
	e := n.guest(ctx, "networkChoose", map[string]any{"addresses": addresses, "cancelled": false}, &chosen)
	if ctx.Err() != nil {
		return wireAddress{}, errors.New("media_cancelled")
	}
	return chosen.Address, e
}
func latin1(s string) string {
	var out strings.Builder
	for _, b := range []byte(s) {
		out.WriteRune(rune(b))
	}
	return out.String()
}
func wireLatin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		out = append(out, byte(r))
	}
	return out
}
func (n Network) open(ctx context.Context, h hop, address wireAddress) (Resource, error) {
	var state wireState
	if e := json.Unmarshal(h.State, &state); e != nil {
		return Resource{}, e
	}
	dial := n.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	family := "tcp6"
	if address.Family == 4 {
		family = "tcp4"
	}
	raw, e := dial(ctx, family, net.JoinHostPort(address.Address, h.Port))
	if e != nil {
		return Resource{}, networkIOError(ctx)
	}
	var conn net.Conn = raw
	stopped, joined := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(stopped); raw.Close() }) }
	finish := func() { stop(); <-joined }
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			raw.Close()
		case <-stopped:
		}
	}()
	fail := func(e error) (Resource, error) { finish(); return Resource{}, e }
	if h.TLS {
		config := &tls.Config{}
		if n.TLSConfig != nil {
			config = n.TLSConfig.Clone()
		}
		config.ServerName = h.Hostname
		config.NextProtos = []string{"http/1.1"}
		secure := tls.Client(raw, config)
		if e = secure.HandshakeContext(ctx); e != nil {
			return fail(networkIOError(ctx))
		}
		conn = secure
	}
	var body []byte
	if state.Body != nil {
		body, e = base64.StdEncoding.DecodeString(*state.Body)
		if e != nil {
			return fail(errors.New("invalid_network_request"))
		}
	}
	// Node's default agent uses keep-alive. We do not pool sockets across calls;
	// Finish closes this connection after exact cached consumption.
	var wire strings.Builder
	fmt.Fprintf(&wire, "%s %s HTTP/1.1\r\n", state.Method, h.Path)
	for _, field := range h.WireHeaders {
		wire.WriteString(field[0])
		wire.WriteString(": ")
		wire.Write(wireLatin1(field[1]))
		wire.WriteString("\r\n")
	}

	fmt.Fprintf(&wire, "Host: %s\r\n", h.Host)
	if state.Body != nil {
		fmt.Fprintf(&wire, "content-length: %d\r\n", len(body))
	} else if state.Method == "POST" || state.Method == "PUT" || state.Method == "PATCH" {
		wire.WriteString("Content-Length: 0\r\n")
	}
	wire.WriteString("Connection: keep-alive\r\n\r\n")
	// Write and response parsing proceed concurrently: servers may send a final
	// response before a large request body finishes. Finish joins the writer.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		if _, err := io.WriteString(conn, wire.String()); err == nil {
			_, err = io.Copy(conn, bytes.NewReader(body))
			if err != nil {
				raw.Close()
			}
		} else {
			raw.Close()
		}
	}()
	baseFinish := finish
	finish = func() { baseFinish(); <-writeDone }
	reader := bufio.NewReader(conn)
	for {
		response, remaining, fields, err := readResponse(reader, state.Method)
		if err != nil {
			return fail(networkIOError(ctx))
		}
		if response.StatusCode == 101 {
			return fail(ErrHTTP101Pending)
		}
		if response.StatusCode >= 100 && response.StatusCode < 200 {
			reader = remaining
			continue
		}
		rawHeaders := []string{}
		for _, f := range fields {
			rawHeaders = append(rawHeaders, latin1(f.name), latin1(strings.TrimRight(f.value, " \t")))
		}
		var normalized struct {
			Headers     map[string]any `json:"headers"`
			ContentType string         `json:"contentType"`
			RawHeaders  []string       `json:"rawHeaders"`
		}
		if err = n.guest(ctx, "networkHeaders", map[string]any{"rawHeaders": rawHeaders}, &normalized); err != nil {
			return fail(err)
		}
		stream := &responseSource{body: response.Body, close: stop, join: finish}
		return Resource{Source: stream, StatusCode: response.StatusCode, Headers: normalized.Headers, RawHeaders: normalized.RawHeaders, ContentType: normalized.ContentType, FinalURL: state.URL, Finish: stream.Finish}, nil
	}
}
func networkIOError(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("network_cancelled")
	}
	return errors.New("network_request_failed")
}

// Source reads are joined before the physical lease can be released. Destroy
// only closes the socket; it is idempotent and never waits on an active Next.
type responseSource struct {
	mu      sync.Mutex
	body    io.ReadCloser
	close   func()
	join    func()
	pending error
	once    sync.Once
}

func (s *responseSource) Next(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.pending != nil {
		return nil, s.pending
	}
	buf := make([]byte, 32*1024)
	n, e := s.body.Read(buf)
	if n > 0 {
		s.pending = e
		return buf[:n], nil
	}
	return nil, e
}
func (s *responseSource) Destroy() { s.once.Do(s.close) }
func (s *responseSource) Finish()  { s.Destroy(); s.join(); s.mu.Lock(); s.body.Close(); s.mu.Unlock() }
