// Package publicpage owns literal-address HTTP GET transport for external tools.
package publicpage

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
	"github.com/killerprojecte/HumanEngine/backend/internal/publicip"
	whatwg "github.com/nlnwa/whatwg-url/url"
)

const (
	maxRedirects = 10 // existing WebRead GET defaults
	headerBytes  = 16 * 1024
)

var (
	ErrPrivate  = errors.New("media_address_not_public")
	ErrURL      = errors.New("media_url_not_supported")
	ErrRedirect = errors.New("media_redirect_not_supported")
	ErrFetch    = errors.New("network_request_failed")
)

// Fetcher owns destination selection. No guest decides what to dial, how to
// frame the request, or what is sent to a redirect target.
type Fetcher struct {
	Lookup    pinnedhop.Lookup
	BeginWork func() (func(), bool)
	Dial      func(context.Context, string, string) (net.Conn, error)
	TLS       *tls.Config
}

// Response streams the HTTP entity bytes without decoding Content-Encoding.
// A caller must call Finish even if it does not consume the stream.
type Response struct {
	URL    string
	Status int
	Header http.Header
	Body   *Body
}

func (f Fetcher) Get(ctx context.Context, locator string) (Response, error) {
	u, err := whatwg.Parse(locator)
	if err != nil || !allowedURL(u, locator) {
		return Response{}, ErrURL
	}
	for redirects := 0; ; redirects++ {
		response, err := f.open(ctx, u)
		if err != nil {
			return Response{}, err
		}
		switch response.Status {
		case 301, 302, 303, 307, 308:
			location := response.Header.Get("Location")
			response.Body.Finish()
			if redirects == maxRedirects || location == "" {
				return Response{}, ErrRedirect
			}
			next, err := u.Parse(location)
			if err != nil || !allowedURL(next, next.Href(false)) ||
				u.Protocol() == "https:" && next.Protocol() != "https:" {
				return Response{}, ErrRedirect
			}
			u = next
		default:
			return response, nil
		}
	}
}

func allowedURL(u *whatwg.Url, raw string) bool {
	n, err := jsjson.UTF16Len(raw)
	return u != nil && err == nil && n > 0 && n <= 16384 &&
		(u.Protocol() == "http:" || u.Protocol() == "https:") &&
		u.Hostname() != "" && u.Username() == "" && u.Password() == "" && u.Hash() == ""
}

func (f Fetcher) address(ctx context.Context, host string) (netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		if !publicip.Allowed(literal) {
			return netip.Addr{}, ErrPrivate
		}
		return literal, nil
	}
	answers, err := pinnedhop.Resolve(ctx, host, 5*time.Second, f.Lookup, f.BeginWork)
	if err != nil {
		return netip.Addr{}, err
	}
	var chosen netip.Addr
	for _, addr := range answers {
		if !publicip.Allowed(addr) {
			return netip.Addr{}, ErrPrivate
		}
		if !chosen.IsValid() || addr.Is4() && !chosen.Is4() {
			chosen = addr
		}
	}
	if !chosen.IsValid() {
		return netip.Addr{}, ErrPrivate
	}
	return chosen, nil
}

func (f Fetcher) open(ctx context.Context, u *whatwg.Url) (Response, error) {
	rawPath := u.Pathname()
	path, err := url.PathUnescape(rawPath)
	if err != nil {
		return Response{}, ErrURL
	}
	query, hasQuery := u.Search(), strings.Contains(u.Href(false), "?")
	reqURL := &url.URL{Path: path, RawPath: rawPath, RawQuery: strings.TrimPrefix(query, "?"), ForceQuery: hasQuery && query == ""}
	if reqURL.EscapedPath() != rawPath {
		return Response{}, ErrURL
	}
	host := strings.Trim(u.Hostname(), "[]")
	ip, err := f.address(ctx, host)
	if err != nil {
		return Response{}, err
	}
	family := 6
	if ip.Is4() {
		family = 4
	}
	tls := u.Protocol() == "https:"
	port := u.Port()
	if port == "" {
		port = "80"
		if tls {
			port = "443"
		}
	}
	conn, stop, finish, err := pinnedhop.Open(ctx, f.Dial, f.TLS, family, ip.String(), port, host, tls)
	if err != nil {
		return Response{}, requestError(ctx)
	}
	fail := func(err error) (Response, error) { finish(); return Response{}, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	req := &http.Request{Method: http.MethodGet, Host: u.Host(), URL: reqURL,
		Header: http.Header{"User-Agent": {""}}, Close: true}
	if err := req.Write(conn); err != nil {
		return fail(requestError(ctx))
	}
	limit := &io.LimitedReader{R: conn, N: headerBytes}
	reader := bufio.NewReader(limit)
	for {
		response, err := http.ReadResponse(reader, req)
		if err != nil {
			return fail(requestError(ctx))
		}
		if response.StatusCode == http.StatusSwitchingProtocols {
			// An unsolicited protocol switch is not a page or an upgraded tunnel.
			return fail(ErrFetch)
		}
		if response.StatusCode >= 200 {
			limit.N = math.MaxInt64
			return Response{URL: u.Href(false), Status: response.StatusCode, Header: response.Header,
				Body: &Body{reader: response.Body, stop: stop, join: finish}}, nil
		}
		limit.N = headerBytes - int64(reader.Buffered())
	}
}

func requestError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrFetch
}

// Body implements the turn cache's owning stream contract. Closing the socket
// unblocks an in-flight iterator before Finish joins and releases the body.
type Body struct {
	mu         sync.Mutex
	reader     io.ReadCloser
	pending    error
	stop, join func()
	once       sync.Once
}

func (b *Body) Next(ctx context.Context) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.pending != nil {
		return nil, b.pending
	}
	buf := make([]byte, 32*1024)
	n, err := b.reader.Read(buf)
	if n > 0 {
		b.pending = err
		return buf[:n], nil
	}
	return nil, err
}

func (b *Body) Destroy() { b.stop() }

func (b *Body) Finish() {
	b.once.Do(func() {
		b.join()
		b.mu.Lock()
		defer b.mu.Unlock()
		_ = b.reader.Close()
		b.pending = io.EOF
	})
}
