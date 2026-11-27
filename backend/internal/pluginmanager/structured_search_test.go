package pluginmanager

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

func TestStructuredTransportFixedTLSAndStream(t *testing.T) {
	const secret = "synthetic-tavily-key"
	var hits, dials atomic.Int32
	var endpoint atomic.Bool
	entity := `{"results":[],"payload":"` + strings.Repeat("x", 9000) + "保存的原始字节" + `"}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Host != "example.com" || r.Method != "POST" || r.URL.Path != "/search" ||
			r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("Cookie") != "" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected provider request metadata: %s %s %s", r.Host, r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"query":"synthetic"}` {
			t.Errorf("unexpected request body: %v", err)
		}
		if endpoint.Load() {
			http.Redirect(w, r, "https://example.com/redirect-must-not-open", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		encoder := gzip.NewWriter(w)
		_, _ = io.WriteString(encoder, entity)
		_ = encoder.Close()
	}))
	defer server.Close()
	route, err := parseStructuredEndpoint("https://example.com/search")
	if err != nil {
		t.Fatal(err)
	}
	owner := newStructuredTransport(t.Context(), route, secret)
	tlsConfig := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	owner.pool.TLSClientConfig = tlsConfig // httptest's synthetic CA and example.com SAN.
	owner.pool.DialContext = func(ctx context.Context, family, address string) (net.Conn, error) {
		dials.Add(1)
		if address != "example.com:443" {
			return nil, errors.New("unexpected route")
		}
		return (&net.Dialer{}).DialContext(ctx, family, server.Listener.Addr().String())
	}
	defer func() {
		if err := owner.CloseAndWait(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	cacheCalls := 0
	cache := func(ctx context.Context, body toolresults.Source, options toolresults.Options) (toolresults.Result, error) {
		cacheCalls++
		if options.Encoding != toolresults.UTF8 || *options.PreviewBytes != 1024 || *options.ContentType != "application/json" {
			t.Fatal("wrong cache options", options)
		}
		var all strings.Builder
		for {
			chunk, err := body.Next(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				return toolresults.Result{}, err
			}
			all.Write(chunk)
		}
		if all.String() != entity {
			t.Fatal("provider entity not streamed in full")
		}
		return toolresults.Result{ResultID: "fixture", TotalBytes: int64(all.Len())}, nil
	}
	finish := func(raw []byte) ([]byte, error) { return raw, nil }
	search := func() structuredResponse {
		t.Helper()
		raw, err := executeStructuredSearch(t.Context(), "tavily", 5, 4096, []byte(`{"query":"synthetic"}`), owner.post, cache, finish)
		if err != nil || strings.Contains(string(raw), secret) {
			t.Fatalf("search failed or included its credential: %v", err)
		}
		var parsed struct{ Observation structuredResponse }
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatal(err)
		}
		return parsed.Observation
	}
	first := search()
	if !first.Partial || first.Text != "" || first.Body == nil || first.Body.TotalBytes != int64(len(entity)) || first.Body.ResultID != "fixture" {
		t.Fatalf("wrong partial cache observation: %+v", first)
	}
	endpoint.Store(true)
	second := search()
	if second.Body != nil || cacheCalls != 1 || hits.Load() != 2 || dials.Load() != 1 {
		t.Fatalf("redirect was followed, uncached body exposed or pooled connection discarded: %+v calls=%d hits=%d dials=%d", second, cacheCalls, hits.Load(), dials.Load())
	}
}

func TestStructuredTransportRefusesPrivateAddressBeforeAuthentication(t *testing.T) {
	route, err := parseStructuredEndpoint("https://127.0.0.1/search")
	if err != nil {
		t.Fatal(err)
	}
	owner := newStructuredTransport(t.Context(), route, "synthetic-tavily-key")
	defer owner.Dispose()
	_, err = owner.post(t.Context(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "structured_address_not_public") {
		t.Fatalf("unexpected route error: %v", err)
	}
	for _, raw := range []string{"http://example.com/search", "https://example.com:8443/", "https://user@example.com/", "https://example.com/search?q=bad", "https://example.com/#"} {
		if _, err := parseStructuredEndpoint(raw); err == nil {
			t.Fatal("unexpected endpoint", raw)
		}
	}
}
