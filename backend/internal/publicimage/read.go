// Package publicimage reads public image URLs over a pinned, guarded transport.
package publicimage

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
	"github.com/killerprojecte/HumanEngine/backend/internal/publicip"
	whatwg "github.com/nlnwa/whatwg-url/url"
)

const (
	publicImageMaxBytes = 64 * 1024 * 1024
	HeaderBytes         = 16 * 1024 // pinned Node http.maxHeaderSize
)

// Reader is the shared public-image GET profile. A retained-image owner
// supplies the source/generation guard and owns the joinable resolver.
type Reader struct {
	Lookup    pinnedhop.Lookup
	BeginWork func() (func(), bool)
	Dial      func(context.Context, string, string) (net.Conn, error)
	TLS       *tls.Config
}

func (r Reader) Read(ctx context.Context, locator string, before func(context.Context) error) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u, err := whatwg.Parse(locator)
	if err != nil {
		return nil, errors.New("invalid_media_reference")
	}
	if !publicImageURL(u, locator) {
		return nil, errors.New("media_url_not_supported")
	}
	for hop := 0; ; hop++ {
		// The stdlib emits the canonical escaped path. Reject paths it
		// cannot represent exactly, before any DNS or socket operation.
		rawPath := u.Pathname()
		path, err := url.PathUnescape(rawPath)
		if err != nil {
			return nil, errors.New("invalid_media_reference")
		}
		reqURL := &url.URL{Path: path, RawPath: rawPath, RawQuery: strings.TrimPrefix(u.Search(), "?")}
		if reqURL.EscapedPath() != rawPath {
			return nil, errors.New("invalid_media_reference")
		}
		hostname := strings.Trim(u.Hostname(), "[]")
		ip, err := r.address(ctx, hostname, before)
		if err != nil {
			return nil, err
		}
		if err := before(ctx); err != nil {
			return nil, err
		}
		bytes, location, err := r.hop(ctx, u, reqURL, ip, hostname, hop == 3, before)
		if err != nil || location == "" {
			return bytes, err
		}
		next, err := u.Parse(location)
		if err != nil || !publicImageURL(next, next.Href(false)) || u.Protocol() == "https:" && next.Protocol() != "https:" {
			return nil, errors.New("media_redirect_not_supported")
		}
		u = next
	}
}

// IsLocator recognizes the HTTP forms that the reader qualifies before DNS.
func IsLocator(value string) bool {
	return len(value) >= 5 && strings.EqualFold(value[:5], "http:") ||
		len(value) >= 6 && strings.EqualFold(value[:6], "https:")
}

func publicImageURL(u *whatwg.Url, original string) bool {
	units, err := jsjson.UTF16Len(original)
	return err == nil && units <= 16384 && (u.Protocol() == "https:" || u.Protocol() == "http:") &&
		u.Username() == "" && u.Password() == "" && u.Hash() == ""
}

func (r Reader) address(ctx context.Context, hostname string, before func(context.Context) error) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(hostname); err == nil {
		return publicImageAddress([]netip.Addr{ip})
	}
	if err := before(ctx); err != nil {
		return netip.Addr{}, err
	}
	addresses, err := pinnedhop.Resolve(ctx, hostname, 5*time.Second, r.Lookup, r.BeginWork)
	if err != nil {
		return netip.Addr{}, err
	}
	return publicImageAddress(addresses)
}

func publicImageAddress(addresses []netip.Addr) (netip.Addr, error) {
	var chosen netip.Addr
	for _, addr := range addresses {
		if !publicip.Allowed(addr) {
			return netip.Addr{}, errors.New("media_address_not_public")
		}
		if !chosen.IsValid() || addr.Is4() && !chosen.Is4() {
			chosen = addr
		}
	}
	if !chosen.IsValid() {
		return netip.Addr{}, errors.New("media_address_not_public")
	}
	return chosen, nil
}

func (r Reader) hop(ctx context.Context, u *whatwg.Url, reqURL *url.URL, ip netip.Addr, hostname string, last bool, before func(context.Context) error) ([]byte, string, error) {
	secure := u.Protocol() == "https:"
	family := 6
	if ip.Is4() {
		family = 4
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if secure {
			port = "443"
		}
	}
	conn, _, finish, err := pinnedhop.Open(ctx, r.Dial, r.TLS, family, ip.String(), port, hostname, secure)
	if err != nil {
		return nil, "", publicImageIOError(ctx)
	}
	defer finish()
	// Dial and TLS can block; requalify the retained source immediately
	// before transmitting the GET. No credentials or caller headers.
	if err := before(ctx); err != nil {
		return nil, "", err
	}
	req := &http.Request{Method: http.MethodGet, Host: u.Host(), URL: reqURL,
		Header: http.Header{"User-Agent": {""}}}
	if err := req.Write(conn); err != nil {
		return nil, "", publicImageIOError(ctx)
	}

	// Preserve the source Node header budget without a second HTTP parser. After
	// final headers, the very same buffered reader becomes the body reader.
	limit := &io.LimitedReader{R: conn, N: HeaderBytes}
	reader := bufio.NewReader(limit)
	var resp *http.Response
	for {
		resp, err = http.ReadResponse(reader, req)
		if err != nil {
			return nil, "", publicImageIOError(ctx)
		}
		if resp.StatusCode < 100 || resp.StatusCode >= 200 || resp.StatusCode == http.StatusSwitchingProtocols {
			break
		}
		limit.N = HeaderBytes - int64(reader.Buffered())
	}
	switch resp.StatusCode {
	case 301, 302, 303, 307, 308:
		location := resp.Header.Get("Location")
		if last || location == "" {
			return nil, "", errors.New("media_redirect_limit")
		}
		return nil, location, nil
	case http.StatusOK:
	default:
		return nil, "", HTTPStatus(resp.StatusCode)
	}
	if resp.ContentLength > publicImageMaxBytes {
		return nil, "", errors.New("media_size_limit")
	}
	limit.N = math.MaxInt64
	bytes, err := io.ReadAll(io.LimitReader(resp.Body, publicImageMaxBytes+1))
	if err != nil || ctx.Err() != nil {
		return nil, "", publicImageIOError(ctx)
	}
	if len(bytes) > publicImageMaxBytes {
		return nil, "", errors.New("media_size_limit")
	}
	if len(bytes) == 0 {
		return nil, "", errors.New("invalid_image_data")
	}
	return bytes, "", nil
}

func publicImageIOError(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("media_cancelled")
	}
	return errors.New("media_fetch_failed")
}

type HTTPStatus int

func (status HTTPStatus) Error() string { return fmt.Sprintf("public_http_%d", int(status)) }

// Only a typed upstream 403 marks a remote locator eligible for exact refresh.
func (status HTTPStatus) Is(target error) bool {
	return status == http.StatusForbidden && target == botruntime.ErrBrowserImageLocatorExpired
}
