package snowlumaruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
)

func TestBrowserTokenPublicReturnActualSource(t *testing.T) {
	var source struct {
		Scene botruntime.Scene
		Asset string
		Cases []struct {
			Name, Bytes, Error string
			Reply              json.RawMessage
			Cancel, Fail       bool
			URLs               []string
			Actions            []struct{ Action, File string }
		}
	}
	raw, err := os.ReadFile("testdata/token-public-return-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &source); err != nil || len(source.Cases) != 8 {
		t.Fatalf("invalid browser continuation fixture: %v", err)
	}
	media, err := nativeImageDescriptor(source.Scene, source.Asset)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range source.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reply, err := jsjson.ParseBytes(tc.Reply)
			if err != nil {
				t.Fatal(err)
			}
			var urls []string
			actions := 0
			bytes, err := readTokenOrInlineDescriptor(ctx, media, str(media, "file"), func(_ context.Context, token string) (any, error) {
				actions++
				if actions != 1 || len(tc.Actions) != 1 || tc.Actions[0].Action != "get_image" || tc.Actions[0].File != token {
					t.Fatalf("unexpected token action %q", token)
				}
				if tc.Cancel {
					cancel()
				}
				return reply, nil
			}, browserTokenContinuation(true, func(_ context.Context, locator string) ([]byte, error) {
				urls = append(urls, locator)
				if tc.Fail {
					return nil, errors.New("synthetic_fetch_failed")
				}
				return []byte("remote bytes"), nil
			}))
			failure := ""
			if err != nil {
				failure = err.Error()
			}
			if errors.Is(err, context.Canceled) {
				failure = "media_cancelled"
			}
			if actions != 1 || !slices.Equal(urls, tc.URLs) || string(bytes) != tc.Bytes || failure != tc.Error {
				t.Fatalf("action=%d URLs=%v bytes=%q err=%q, want %v %q %q", actions, urls, bytes, failure, tc.URLs, tc.Bytes, tc.Error)
			}
			if len(tc.URLs) > 0 {
				unsupported := browserTokenContinuation(false, func(context.Context, string) ([]byte, error) {
					t.Fatal("unsupported build reached the public reader")
					return nil, nil
				})
				for _, read := range []selectedImageRead{unsupported, inlineTokenContinuation} {
					_, err := fetchImageToken(t.Context(), "opaque", func(context.Context, string) (any, error) { return reply, nil }, read)
					if err == nil || err.Error() != "image_download_url_unavailable" {
						t.Fatalf("unsupported browser or Vision changed public-return error: %v", err)
					}
				}
			}
		})
	}
}

func TestBrowserTokenPublicReturnRequalifiesBeforeDNS(t *testing.T) {
	for _, deny := range []bool{false, true} {
		name := "qualifies-and-fetches"
		if deny {
			name = "revoked-after-get-image"
		}
		t.Run(name, func(t *testing.T) {
			revoked, actions, lookups, dials := false, 0, 0, 0
			guard := func(context.Context) error {
				if revoked {
					return botruntime.ErrDisplayImageUnavailable
				}
				return nil
			}
			wire := make(chan error, 1)
			reader := publicImageReader{
				beginWork: func() (func(), bool) { return func() {}, true },
				lookup: func(_ context.Context, host string) pinnedhop.LookupJob {
					if host != "fixture.invalid" {
						t.Fatalf("unexpected DNS host %q", host)
					}
					lookups++
					result := make(chan pinnedhop.DNSResult, 1)
					drained := make(chan struct{})
					result <- pinnedhop.DNSResult{Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
					close(result)
					close(drained)
					return pinnedhop.LookupJob{Result: result, Drained: drained}
				},
				dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					dials++
					if network != "tcp4" || address != "8.8.8.8:80" {
						t.Fatalf("unexpected pinned dial %s %s", network, address)
					}
					client, server := net.Pipe()
					if deadline, ok := ctx.Deadline(); ok {
						_ = server.SetDeadline(deadline)
					}
					go func() {
						defer server.Close()
						request, err := http.ReadRequest(bufio.NewReader(server))
						if err != nil {
							wire <- err
							return
						}
						if request.Method != "GET" || request.Host != "fixture.invalid" || request.RequestURI != "/chosen" || len(request.Header) != 0 {
							wire <- errors.New("unexpected public request or leaked credentials")
							return
						}
						_, err = io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\nc")
						wire <- err
					}()
					return client, nil
				},
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			bytes, err := fetchImageToken(ctx, "opaque", func(call context.Context, _ string) (any, error) {
				actions++
				if err := guard(call); err != nil {
					return nil, err
				}
				revoked = deny
				return obj("url", "http://fixture.invalid/chosen", "file", "base64://YQ=="), nil
			}, browserTokenContinuation(true, func(call context.Context, locator string) ([]byte, error) {
				return reader.read(call, locator, guard)
			}))
			if deny {
				if !errors.Is(err, botruntime.ErrDisplayImageUnavailable) || actions != 1 || lookups != 0 || dials != 0 || len(bytes) != 0 {
					t.Fatalf("revoked source reached second-hop IO: actions=%d lookup=%d dial=%d bytes=%q err=%v", actions, lookups, dials, bytes, err)
				}
				return
			}
			if err != nil || actions != 1 || lookups != 1 || dials != 1 || string(bytes) != "c" {
				t.Fatalf("wrong composition: actions=%d lookup=%d dial=%d bytes=%q err=%v", actions, lookups, dials, bytes, err)
			}
			if err := <-wire; err != nil {
				t.Fatal(err)
			}
		})
	}
}
