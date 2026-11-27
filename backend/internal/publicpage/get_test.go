package publicpage

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
)

func answers(addresses ...string) pinnedhop.Lookup {
	return func(context.Context, string) pinnedhop.LookupJob {
		ready, drained := make(chan pinnedhop.DNSResult, 1), make(chan struct{})
		ips := make([]netip.Addr, len(addresses))
		for i, address := range addresses {
			ips[i] = netip.MustParseAddr(address)
		}
		ready <- pinnedhop.DNSResult{Addresses: ips}
		close(drained)
		return pinnedhop.LookupJob{Result: ready, Drained: drained}
	}
}

func admission() (func(), bool) { return func() {}, true }

func TestVerifiedGETStreamsWithoutTransformingBytes(t *testing.T) {
	var hits atomic.Int32
	body := "<html><h1>Heading</h1>" + strings.Repeat("paragraph ", 10000) + "准确字节</html>"
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != "GET" || r.Host != "page.fixture.invalid" || r.URL.RequestURI() != "/manual%2Fpage?tab=1" ||
			r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected request", r.Method, r.Host, r.URL.RequestURI(), r.Header)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	defer page.Close()
	f := Fetcher{BeginWork: admission, Lookup: answers("2606:4700:4700::1111", "1.1.1.1"),
		Dial: func(ctx context.Context, family, address string) (net.Conn, error) {
			if family != "tcp4" || address != "1.1.1.1:80" {
				t.Errorf("unexpected admitted literal %s %s", family, address)
				return nil, errors.New("invalid fixture dial")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp4", strings.TrimPrefix(page.URL, "http://"))
		}}
	result, err := f.Get(t.Context(), "http://page.fixture.invalid/manual%2Fpage?tab=1")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Finish()
	var got strings.Builder
	for {
		chunk, err := result.Body.Next(t.Context())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got.Write(chunk)
	}
	if hits.Load() != 1 || result.Status != 200 || result.Header.Get("Content-Type") != "text/html; charset=utf-8" || got.String() != body {
		t.Fatal("wrong status, route count, entity type or physical body")
	}
}

func TestUnapprovedDestinationsDoNotOpenSockets(t *testing.T) {
	f := Fetcher{BeginWork: admission, Lookup: answers("1.1.1.1", "10.0.0.1"),
		Dial: func(context.Context, string, string) (net.Conn, error) {
			t.Error("rejected destination attempted a dial")
			return nil, errors.New("unexpected dial")
		}}
	for _, url := range []string{"http://127.0.0.1/", "http://mixed.fixture.invalid/"} {
		if _, err := f.Get(t.Context(), url); !errors.Is(err, ErrPrivate) {
			t.Fatal(url, err)
		}
	}
	for _, url := range []string{"file:///manual.html", "https://example.invalid/page#section"} {
		if _, err := f.Get(t.Context(), url); !errors.Is(err, ErrURL) {
			t.Fatal(url, err)
		}
	}
}

func TestRedirectQualifiesBeforeOpeningAnotherSocket(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
	}))
	defer server.Close()
	var dials atomic.Int32
	f := Fetcher{BeginWork: admission, Lookup: answers("1.1.1.1"),
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "http://"))
		}}
	if _, err := f.Get(t.Context(), "http://page.fixture.invalid/"); !errors.Is(err, ErrPrivate) || hits.Load() != 1 || dials.Load() != 1 {
		t.Fatal("redirect was not rejected before I/O", err, hits.Load(), dials.Load())
	}
}
