//go:build web_read_candidate

package host_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/plugins/web-read/host"
)

func TestUnsolicited101ClosesPhysicalStreamAndWaitsForCancellation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		peerFIN  bool
		cancelBy string
	}{
		{name: "peer-fin", peerFIN: true, cancelBy: "timeout"},
		{name: "quiet-timeout", cancelBy: "timeout"},
		{name: "quiet-caller", cancelBy: "caller"},
		{name: "quiet-owner", cancelBy: "owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			peerClosed := make(chan error, 1)
			go func() {
				conn, err := server.Accept()
				if err != nil {
					peerClosed <- err
					return
				}
				defer conn.Close()
				reader := bufio.NewReader(conn)
				if _, err = http.ReadRequest(reader); err != nil {
					peerClosed <- err
					return
				}
				wire := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: fixture\r\n\r\n"
				if tc.peerFIN {
					wire += "fixture"
				}
				if _, err = io.WriteString(conn, wire); err != nil {
					peerClosed <- err
					return
				}
				if tc.peerFIN {
					if err = conn.(*net.TCPConn).CloseWrite(); err != nil {
						peerClosed <- err
						return
					}
				}
				_, err = reader.ReadByte()
				if !errors.Is(err, io.EOF) && !(tc.peerFIN && errors.Is(err, syscall.ECONNRESET)) {
					peerClosed <- errors.New("expected client socket close without protocol bytes")
					return
				}
				peerClosed <- nil
			}()

			caller, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			owner, cancelOwner := context.WithCancel(context.Background())
			defer cancelOwner()
			var headersOrRedirect atomic.Bool
			realGuest := admitNative(nativeGuest)
			network := host.Network{
				BeginWork: noWork,
				Life:      owner,
				CallGuestWithAdmission: func(ctx, life context.Context, wire []byte) ([]byte, error) {
					var input struct{ Operation string }
					if err := json.Unmarshal(wire, &input); err != nil {
						return nil, err
					}
					if input.Operation == "networkHeaders" || input.Operation == "networkRedirect" {
						headersOrRedirect.Store(true)
					}
					return realGuest(ctx, life, wire)
				},
				Lookup: testLookup(func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
				}),
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp4", server.Addr().String())
				},
			}
			type outcome struct {
				value host.Resource
				err   error
			}
			result := make(chan outcome, 1)
			go func() {
				value, err := network.Fetch(caller, json.RawMessage(`{"url":"http://synthetic.fixture.invalid/"}`), time.Second)
				result <- outcome{value, err}
			}()

			select {
			case err := <-peerClosed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("physical stream did not close")
			}
			select {
			case early := <-result:
				t.Fatalf("101 settled before source cancellation: %+v", early)
			default:
			}
			switch tc.cancelBy {
			case "caller":
				cancelCaller()
			case "owner":
				cancelOwner()
			}
			select {
			case out := <-result:
				if out.err == nil || out.err.Error() != "network_cancelled" || out.value.Source != nil ||
					out.value.StatusCode != 0 || out.value.Finish != nil || headersOrRedirect.Load() {
					t.Fatalf("101 escaped into an ordinary response: %+v", out)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("logical request did not settle")
			}
		})
	}
}
