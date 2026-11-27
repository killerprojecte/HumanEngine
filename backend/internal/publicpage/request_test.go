package publicpage

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStructuredRequestPreservesEncodedEntityAndRawHeaders(t *testing.T) {
	const threshold = 1 << 20
	var plain [threshold + 16384]byte
	rng := rand.NewChaCha8([32]byte{1})
	_, _ = rng.Read(plain[:])
	var wire bytes.Buffer
	zip := gzip.NewWriter(&wire)
	_, _ = zip.Write(plain[:])
	if err := zip.Close(); err != nil || wire.Len() <= threshold {
		t.Fatal("fixture must be larger than the former byte limit when compressed", err)
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Host != "fixture.example" || r.URL.RequestURI() != "/manual?view=raw" ||
			r.Header.Get("Accept-Encoding") != "gzip" || r.Header.Get("X-Input") != "request" {
			t.Errorf("unexpected %s %s %s", r.Method, r.Host, r.URL.RequestURI())
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(rw, "HTTP/1.1 418 Synthetic\r\nX-Fixture: first\r\nContent-Encoding: gzip\r\nx-Fixture: second\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", wire.Len())
		if r.Method != http.MethodHead {
			_, _ = rw.Write(wire.Bytes())
		}
		_ = rw.Flush()
	}))
	defer server.Close()
	f := Fetcher{BeginWork: admission, Lookup: answers("1.1.1.1"),
		Dial: func(ctx context.Context, family, address string) (net.Conn, error) {
			if family != "tcp4" || address != "1.1.1.1:80" {
				return nil, errors.New("unexpected address")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp4", server.Listener.Addr().String())
		}}
	headers := []string{"X-Fixture", "first", "Content-Encoding", "gzip", "x-Fixture", "second", "Content-Type", "application/octet-stream", "Content-Length", fmt.Sprint(wire.Len())}
	for _, method := range []string{"GET", "HEAD"} {
		t.Run(method, func(t *testing.T) {
			input := json.RawMessage(fmt.Sprintf(`{"url":"http://fixture.example/manual?view=raw","method":%q,"headers":{"accept-encoding":"gzip","x-input":"request"}}`, method))
			result, err := f.Request(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			defer result.Body.Finish()
			if result.Status != 418 || !slices.Equal(result.RawHeaders, headers) {
				t.Fatalf("wrong source metadata: %d %q", result.Status, result.RawHeaders)
			}
			var got bytes.Buffer
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
			if method == "HEAD" && got.Len() != 0 || method == "GET" && !bytes.Equal(got.Bytes(), wire.Bytes()) {
				t.Fatal("not the exact entity", got.Len())
			}
		})
	}
	if hits.Load() != 2 {
		t.Fatal("each request must reach the origin once", hits.Load())
	}
}

func TestStructuredRequestRejectsMethodsHeadersAndPrivateHops(t *testing.T) {
	f := Fetcher{BeginWork: admission, Lookup: answers("8.8.8.8", "127.0.0.1"),
		Dial: func(context.Context, string, string) (net.Conn, error) {
			t.Error("rejected request opened a socket")
			return nil, errors.New("fixture refused dial")
		}}
	cases := []struct {
		raw  string
		want error
	}{
		{`{"url":"http://origin.example/","method":"POST"}`, ErrInput},
		{`{"url":"http://origin.example/","method":"HEAD","body":"not allowed"}`, ErrInput},
		{`{"url":"http://origin.example/","headers":{"transfer-encoding":"chunked"}}`, ErrHeader},
		{`{"url":"http://origin.example/","headers":{"x-fixture":"safe\r\nunsafe"}}`, ErrHeader},
		{`{"url":"http://origin.example/"}`, ErrPrivate},
	}
	for _, c := range cases {
		if _, err := f.Request(t.Context(), json.RawMessage(c.raw)); !errors.Is(err, c.want) {
			t.Error(c.raw, err, c.want)
		}
	}
}

func BenchmarkResponseEntity32KiB(b *testing.B) {
	input := strings.Repeat("x", 16<<20)
	reader := strings.NewReader(input)
	body := Body{reader: io.NopCloser(reader)}
	ctx := context.Background()
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	for b.Loop() {
		reader.Reset(input)
		body.pending = nil
		for {
			if _, err := body.Next(ctx); err == io.EOF {
				break
			}
		}
	}
}

func TestStructuredRequestOptionsAndExplicitBodyFraming(t *testing.T) {
	for _, tc := range []struct{ method, encoding, input, want string }{
		{"OPTIONS", "base64", "4pSoAA==", "┨\x00"},
		{"GET", "utf8", "", ""},
	} {
		t.Run(tc.method, func(t *testing.T) {
			proof := make(chan error, 1)
			f := Fetcher{Lookup: answers("1.1.1.1"), BeginWork: admission,
				Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
					client, fixture := net.Pipe()
					go func() {
						defer fixture.Close()
						rd := bufio.NewReader(fixture)
						req, err := http.ReadRequest(rd)
						if err != nil {
							proof <- err
							return
						}
						data, err := io.ReadAll(req.Body)
						_ = req.Body.Close()
						if err != nil || string(data) != tc.want || req.Method != tc.method ||
							req.Header.Get("Content-Length") != fmt.Sprint(len(tc.want)) ||
							req.Header.Get("Transfer-Encoding") != "" {
							proof <- fmt.Errorf("incorrect body framing: %q %v", data, err)
							return
						}
						proof <- nil
						_, _ = io.WriteString(fixture, "HTTP/1.1 204 No Content\r\n\r\n")
					}()
					return client, nil
				}}
			raw := fmt.Appendf(nil, `{"url":"http://fixture.example/","method":%q,"bodyEncoding":%q,"body":%q}`, tc.method, tc.encoding, tc.input)
			resp, err := f.Request(t.Context(), raw)
			if wireErr := <-proof; err != nil || wireErr != nil {
				t.Fatalf("unexpected body transport: %v / %v", err, wireErr)
			}
			defer resp.Body.Finish()
			if resp.Status != 204 {
				t.Fatal(resp.Status)
			}
		})
	}
}
