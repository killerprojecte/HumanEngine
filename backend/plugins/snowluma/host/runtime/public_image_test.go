package snowlumaruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
	"github.com/killerprojecte/HumanEngine/backend/internal/publicimage"
)

func TestPublicImageActualSource(t *testing.T) {
	type request struct {
		Address, Port, Servername, Path, Host string
		Family                                int
	}
	var fixture struct {
		MaxHeaderSize int
		Cases         []struct {
			Name, URL, Bytes, Error string
			Addresses, DNS          []string
			Requests                []request
			Responses               []struct {
				Status                  int
				Location, Bytes, Length string
			}
		}
	}
	raw, err := os.ReadFile("testdata/public-image-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.MaxHeaderSize != publicimage.HeaderBytes {
		t.Fatalf("Node header limit=%d, Go=%d", fixture.MaxHeaderSize, publicimage.HeaderBytes)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			lookups := []string{}
			actual := []request{}
			wire := make(chan request, len(tc.Responses))
			var index atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "GET" || len(req.Header) != 0 {
					t.Errorf("unexpected request method/headers: %s %v", req.Method, req.Header)
				}
				item := request{Path: req.RequestURI, Host: req.Host}
				if req.TLS != nil {
					item.Servername = req.TLS.ServerName
				}
				wire <- item
				res := tc.Responses[index.Add(1)-1]
				if res.Location != "" {
					w.Header().Set("Location", res.Location)
				}
				if res.Length != "" {
					w.Header().Set("Content-Length", res.Length)
				}
				status := res.Status
				if status == 0 {
					status = 200
				}
				w.WriteHeader(status)
				body := res.Bytes
				if body == "" {
					body = "original"
				}
				fmt.Fprint(w, body)
			})
			var server *httptest.Server
			reader := publicimage.Reader{
				Lookup: func(_ context.Context, hostname string) pinnedhop.LookupJob {
					lookups = append(lookups, hostname)
					result := make(chan pinnedhop.DNSResult, 1)
					drained := make(chan struct{})
					ips := make([]netip.Addr, len(tc.Addresses))
					for i, addr := range tc.Addresses {
						ips[i] = netip.MustParseAddr(addr)
					}
					result <- pinnedhop.DNSResult{Addresses: ips}
					close(result)
					close(drained)
					return pinnedhop.LookupJob{Result: result, Drained: drained}
				},
				BeginWork: func() (func(), bool) { return func() {}, true },
				Dial: func(ctx context.Context, family, target string) (net.Conn, error) {
					if len(actual) >= len(tc.Requests) {
						return nil, fmt.Errorf("unexpected dial to %s", target)
					}
					addr, port, err := net.SplitHostPort(target)
					if err != nil {
						return nil, err
					}
					f, err := strconv.Atoi(strings.TrimPrefix(family, "tcp"))
					if err != nil {
						return nil, err
					}
					actual = append(actual, request{Address: addr, Port: port, Family: f})
					return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
				},
			}
			if len(tc.Requests) != 0 {
				if tc.Requests[0].Servername != "" {
					server = httptest.NewTLSServer(handler)
					reader.TLS = server.Client().Transport.(*http.Transport).TLSClientConfig
				} else {
					server = httptest.NewServer(handler)
				}
				defer server.Close()
			}
			bytes, err := reader.Read(context.Background(), tc.URL, func(context.Context) error { return nil })
			if len(actual) == len(tc.Requests) && len(wire) == len(actual) {
				for i := range actual {
					item := <-wire
					actual[i].Path, actual[i].Host, actual[i].Servername = item.Path, item.Host, item.Servername
				}
			}
			if !reflect.DeepEqual(lookups, tc.DNS) || !reflect.DeepEqual(actual, tc.Requests) {
				t.Errorf("wire traces:\n DNS=%v want %v\n HTTP=%+v want %+v", lookups, tc.DNS, actual, tc.Requests)
			}
			var failure string
			if err != nil {
				failure = err.Error()
			}
			if string(bytes) != tc.Bytes || failure != tc.Error {
				t.Errorf("result=(%q,%q) want (%q,%q)", bytes, failure, tc.Bytes, tc.Error)
			}
		})
	}
}

// Guard revocation between separately blocking I/O boundaries. The synthetic
// ports model lookup and dial; no production transport override is installed.
func TestPublicImageRequalifiesBeforeIO(t *testing.T) {
	denied := errors.New("retained_source_revoked")
	for _, stage := range []string{"dns", "dial"} {
		t.Run(stage, func(t *testing.T) {
			var revoked bool
			var lookup, dial int
			input, output := net.Pipe()
			defer output.Close()
			r := publicimage.Reader{
				BeginWork: func() (func(), bool) { return func() {}, true },
				Lookup: func(context.Context, string) pinnedhop.LookupJob {
					lookup++
					revoked = stage == "dns"
					result := make(chan pinnedhop.DNSResult, 1)
					drained := make(chan struct{})
					result <- pinnedhop.DNSResult{Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
					close(result)
					close(drained)
					return pinnedhop.LookupJob{Result: result, Drained: drained}
				},
				Dial: func(context.Context, string, string) (net.Conn, error) {
					dial++
					revoked = true
					return output, nil
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := r.Read(ctx, "http://example.com/direct", func(context.Context) error {
				if revoked {
					return denied
				}
				return nil
			})
			if !errors.Is(err, denied) || lookup != 1 || (dial != 0) != (stage == "dial") {
				t.Fatalf("wrong boundary: err=%v lookup=%d dial=%d", err, lookup, dial)
			}
			if stage == "dial" {
				var first [1]byte
				if n, err := input.Read(first[:]); n != 0 || err != io.EOF {
					t.Fatalf("revoked source transmitted: %d, %v", n, err)
				}
			}
			input.Close()
		})
	}
}
