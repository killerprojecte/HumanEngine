//go:build web_read_candidate

package host_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/plugins/web-read/host"
)

func TestNetworkSourceFixtures(t *testing.T) { runNetworkFixtures(t, admitNative(nativeGuest)) }
func TestRealGuestNetworkSourceFixtures(t *testing.T) {
	path := os.Getenv("WEB_READ_WASM")
	if path == "" {
		t.Skip("set WEB_READ_WASM to built reactor")
	}
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	wasm, e := plugins.NewWASMHost(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer wasm.Close(ctx)
	if e = wasm.Enable(ctx, "web-read", data); e != nil {
		t.Fatal(e)
	}
	call, e := wasm.BindCallWithAdmission("web-read")
	if e != nil {
		t.Fatal(e)
	}
	runNetworkFixtures(t, call)
}
func runNetworkFixtures(t *testing.T, call func(context.Context, context.Context, []byte) ([]byte, error)) {
	raw, e := os.ReadFile("../core/testdata/node-network.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []struct {
		Name      string                     `json:"name"`
		URL       string                     `json:"url"`
		Options   map[string]json.RawMessage `json:"options"`
		Responses []string                   `json:"responses"`
		Addresses []struct {
			Address string `json:"address"`
			Family  int    `json:"family"`
		} `json:"addresses"`
		Requests any      `json:"requests"`
		Lookups  []string `json:"lookups"`
		Expected any      `json:"expected"`
	}
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			listener, e := net.Listen("tcp4", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			var mu sync.Mutex
			requests := []any{}
			lookups := []string{}
			index := 0
			var wg sync.WaitGroup
			wg.Add(1)
			serverErrors := make(chan error, 10)
			go func() {
				defer wg.Done()
				for {
					conn, e := listener.Accept()
					if e != nil {
						return
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer conn.Close()
						reader := bufio.NewReader(conn)
						var headerText strings.Builder
						headerNames := []string{}
						for lineNo := 0; ; lineNo++ {
							line, err := reader.ReadString('\n')
							if err != nil {
								serverErrors <- err
								return
							}
							headerText.WriteString(line)
							if line == "\r\n" {
								break
							}
							if lineNo > 0 {
								headerNames = append(headerNames, strings.SplitN(line, ":", 2)[0])
							}
						}
						req, e := http.ReadRequest(bufio.NewReader(io.MultiReader(strings.NewReader(headerText.String()), reader)))
						if e != nil {
							serverErrors <- e
							return
						}
						body, e := io.ReadAll(req.Body)
						if e != nil {
							serverErrors <- e
							return
						}
						headers := map[string]string{"host": req.Host}
						for k, v := range req.Header {
							headers[strings.ToLower(k)] = strings.Join(v, ", ")
						}
						entry := map[string]any{"method": req.Method, "path": req.RequestURI, "headers": headers, "headerNames": headerNames, "body": base64.StdEncoding.EncodeToString(body)}
						mu.Lock()
						requests = append(requests, entry)
						n := index
						index++
						mu.Unlock()
						if n >= len(fixture.Responses) {
							serverErrors <- errors.New("unexpected local connection")
							return
						}
						bytes := []byte{}
						for _, r := range fixture.Responses[n] {
							bytes = append(bytes, byte(r))
						}
						_, e = conn.Write(bytes)
						if e != nil {
							serverErrors <- e
						}
					}()
				}
			}()
			n := host.Network{BeginWork: noWork, Life: context.Background(), CallGuestWithAdmission: call, Lookup: testLookup(func(_ context.Context, name string) ([]netip.Addr, error) {
				lookups = append(lookups, name)
				addresses := []netip.Addr{}
				for _, a := range fixture.Addresses {
					addresses = append(addresses, netip.MustParseAddr(a.Address))
				}
				if fixture.Addresses == nil {
					addresses = append(addresses, netip.MustParseAddr("8.8.8.8"))
				}
				return addresses, nil
			}), DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
				if target != "8.8.8.8:80" {
					return nil, errors.New("unexpected pinned target: " + target)
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp4", listener.Addr().String())
			}}
			request := fixture.Options
			request["url"], _ = json.Marshal(fixture.URL)
			input, _ := json.Marshal(request)
			result, e := n.Fetch(context.Background(), input, 2*time.Second)
			var got any
			if e != nil {
				var typeError interface{ TypeError() bool }
				isTypeError := errors.As(e, &typeError) && typeError.TypeError()
				got = map[string]any{"error": e.Error(), "typeError": isTypeError}
			} else {
				var body []byte
				for {
					chunk, e := result.Source.Next(context.Background())
					body = append(body, chunk...)
					if e == io.EOF {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
				}
				result.Finish()
				got = map[string]any{"statusCode": result.StatusCode, "headers": result.Headers, "rawHeaders": result.RawHeaders, "finalUrl": result.FinalURL, "contentType": result.ContentType, "body": base64.StdEncoding.EncodeToString(body)}
			}
			listener.Close()
			wg.Wait()
			close(serverErrors)
			for e := range serverErrors {
				t.Error(e)
			}
			normalize := func(v any) any { b, _ := json.Marshal(v); var x any; json.Unmarshal(b, &x); return x }
			if !reflect.DeepEqual(normalize(got), fixture.Expected) {
				b, _ := json.Marshal(got)
				want, _ := json.Marshal(fixture.Expected)
				t.Errorf("response\ngot %s\nwant %s", b, want)
			}
			if !reflect.DeepEqual(normalize(requests), fixture.Requests) {
				t.Errorf("requests\ngot %#v\nwant %#v", requests, fixture.Requests)
			}
			if !reflect.DeepEqual(lookups, fixture.Lookups) {
				t.Errorf("lookups got %#v want %#v", lookups, fixture.Lookups)
			}
		})
	}
}

func TestNativeResponseDestroyUnblocksReadAndJoins(t *testing.T) {
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		http.ReadRequest(bufio.NewReader(conn))
		io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n")
		io.Copy(io.Discard, conn)
	}()
	n := host.Network{BeginWork: noWork, Life: context.Background(), CallGuestWithAdmission: admitNative(nativeGuest), Lookup: testLookup(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}), DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp4", listener.Addr().String())
	}}
	r, e := n.Fetch(context.Background(), json.RawMessage(`{"url":"http://fixture.invalid/"}`), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	started, done := make(chan struct{}), make(chan error, 1)
	go func() { close(started); _, e := r.Source.Next(context.Background()); done <- e }()
	<-started
	destroyer, ok := r.Source.(interface{ Destroy() })
	if !ok {
		t.Fatal("source cannot abort native read")
	}
	destroyer.Destroy()
	r.Finish()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("incomplete body reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("destroy failed to interrupt read")
	}
	<-serverDone
}

func noWork() (func(), bool) { return func() {}, true }
func testLookup(fn func(context.Context, string) ([]netip.Addr, error)) host.Lookup {
	return func(ctx context.Context, name string) host.LookupJob {
		result := make(chan host.DNSResult, 1)
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			addresses, e := fn(ctx, name)
			result <- host.DNSResult{Addresses: addresses, Err: e}
		}()
		return host.LookupJob{Result: result, Drained: drained}
	}
}
