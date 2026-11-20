package snowlumaruntime

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

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
	"github.com/killerprojecte/HumanEngine/backend/internal/publicip"
	whatwg "github.com/nlnwa/whatwg-url/url"
)

const (
	publicImageMaxBytes    = 64 * 1024 * 1024
	publicImageHeaderBytes = 16 * 1024 // pinned Node http.maxHeaderSize
)

// publicImageReader is the fixed SnowLuma public-image GET profile. It is
// called only by the retained native browser-image owner, with its
// generation/source guard and joinable resolver.
type publicImageReader struct {
	lookup    pinnedhop.Lookup
	beginWork func() (func(), bool)
	dial      func(context.Context, string, string) (net.Conn, error)
	tls       *tls.Config
}

func (r publicImageReader) read(ctx context.Context, locator string, before func(context.Context) error) ([]byte, error) {
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

func publicImageURL(u *whatwg.Url, original string) bool {
	units, err := jsjson.UTF16Len(original)
	return err == nil && units <= 16384 && (u.Protocol() == "https:" || u.Protocol() == "http:") &&
		u.Username() == "" && u.Password() == "" && u.Hash() == ""
}

func (r publicImageReader) address(ctx context.Context, hostname string, before func(context.Context) error) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(hostname); err == nil {
		return publicImageAddress([]netip.Addr{ip})
	}
	if err := before(ctx); err != nil {
		return netip.Addr{}, err
	}
	addresses, err := pinnedhop.Resolve(ctx, hostname, 5*time.Second, r.lookup, r.beginWork)
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

func (r publicImageReader) hop(ctx context.Context, u *whatwg.Url, reqURL *url.URL, ip netip.Addr, hostname string, last bool, before func(context.Context) error) ([]byte, string, error) {
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
	conn, _, finish, err := pinnedhop.Open(ctx, r.dial, r.tls, family, ip.String(), port, hostname, secure)
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
	limit := &io.LimitedReader{R: conn, N: publicImageHeaderBytes}
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
		limit.N = publicImageHeaderBytes - int64(reader.Buffered())
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
		return nil, "", publicImageHTTPStatus(resp.StatusCode)
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

type publicImageHTTPStatus int

func (status publicImageHTTPStatus) Error() string { return fmt.Sprintf("public_http_%d", int(status)) }
