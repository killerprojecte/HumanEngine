package snowlumatransport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWSNegotiationActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/ws-negotiation-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Extension  string
		Accepted   bool
		SourceCode string
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	peer := exec.Command("node", "--import", "tsx", "generate-negotiation-source.mjs", "--peer")
	var diagnostics bytes.Buffer
	peer.Stderr = &diagnostics
	stdout, err := peer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := peer.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = peer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		if err := peer.Wait(); err != nil {
			t.Errorf("peer: %v %s", err, diagnostics.String())
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("missing peer address")
	}
	var address struct{ URL string }
	if err = json.Unmarshal(scanner.Bytes(), &address); err != nil {
		t.Fatal(err)
	}
	for index, row := range cases {
		t.Run(row.Extension, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			defer transport.CloseIdleConnections()
			connection, _, err := websocket.Dial(ctx, fmt.Sprintf("%s/%d", address.URL, index), &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, CompressionMode: websocket.CompressionContextTakeover})
			if connection != nil {
				connection.CloseNow()
			}
			if !row.Accepted && ctx.Err() != nil {
				t.Fatalf("rejected handshake waited for response body until caller deadline: %v", err)
			}
			if (err == nil) != row.Accepted {
				t.Fatalf("actual handshake %v; source accepted=%t (%s)", err, row.Accepted, row.SourceCode)
			}
		})
	}
}
