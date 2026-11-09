//go:build web_read_candidate && cgo && linux

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	webread "github.com/killerprojecte/HumanEngine/backend/plugins/web-read/core"
)

func TestOSDNSLengthNativeBoundary(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(t.TempDir(), "dns-native-shim.so")
	if output, err := exec.Command(cc, "-std=c11", "-shared", "-fPIC", "-o", shim, "testdata/dns-native-shim.c", "-ldl").CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	for _, kind := range []string{"Go", "NodeSource"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var cmd *exec.Cmd
			if kind == "Go" {
				cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOSDNSLengthNativeChild$", "-test.v")
			} else {
				node, err := exec.LookPath("node")
				if err != nil {
					t.Fatal("actual source oracle requires Node", err)
				}
				cmd = exec.CommandContext(ctx, node, "--import", "tsx", "backend/plugins/web-read/check-dns-length-source.mjs")
				cmd.Dir = filepath.Join("..", "..", "..", "..")
			}
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "LD_PRELOAD=") && !strings.HasPrefix(entry, "HE_DNS_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			events, err := os.CreateTemp(t.TempDir(), "native-calls")
			if err != nil {
				t.Fatal(err)
			}
			defer events.Close()
			cmd.ExtraFiles = []*os.File{events}
			cmd.Env = append(cmd.Env, "LD_PRELOAD="+shim, "HE_DNS_LENGTH_ONLY=1", "HE_DNS_EVENTS=3")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v %s", kind, err, output)
			} else {
				t.Log(string(output))
			}
		})
	}
}

func TestOSDNSLengthNativeChild(t *testing.T) {
	if os.Getenv("HE_DNS_LENGTH_ONLY") != "1" {
		t.Skip("isolated native boundary child only")
	}
	events := os.NewFile(3, "native-calls")
	defer events.Close()
	count := func() int64 {
		t.Helper()
		info, err := events.Stat()
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	for _, unicode := range []bool{false, true} {
		for _, length := range []int{255, 256, 257} {
			t.Run(fmt.Sprintf("unicode_%v_length_%d", unicode, length), func(t *testing.T) {
				host := strings.Repeat("a", length)
				if unicode {
					host = "é." + strings.Repeat("a", length-8)
				}
				request, _ := json.Marshal(map[string]string{"url": "http://" + host + "/"})
				prepared := webread.Invoke(append([]byte(`{"operation":"networkPrepare","timeoutMs":60000,"request":`), append(request, '}')...))
				data, err := json.Marshal(prepared)
				if err != nil {
					t.Fatal(err)
				}
				var h hop
				if err = json.Unmarshal(data, &h); err != nil || len(h.Hostname) != length {
					t.Fatal("guest did not preserve normalized length", string(data), err)
				}
				before := count()
				r, err := NewOSResolver()
				if err != nil {
					t.Fatal(err)
				}
				defer r.CloseAndWait()
				job := r.Lookup(context.Background(), h.Hostname)
				requireDNSDrain(t, job)
				if answer := <-job.Result; answer.Err == nil {
					t.Fatal("fixture must fail lookup")
				}
				var work sync.WaitGroup
				n := Network{Lookup: r.Lookup, Life: context.Background(), BeginWork: func() (func(), bool) { work.Add(1); return work.Done, true }, CallGuestWithAdmission: func(_ context.Context, _ context.Context, in []byte) ([]byte, error) {
					return json.Marshal(webread.Invoke(in))
				}, DialContext: func(context.Context, string, string) (net.Conn, error) {
					t.Error("DNS failure dialled")
					return nil, fmt.Errorf("unexpected dial")
				}}
				_, err = n.Fetch(context.Background(), request, time.Minute)
				if err == nil || err.Error() != "media_dns_failed" {
					t.Fatal("source error changed", err)
				}
				r.CloseAndWait()
				work.Wait()
				want := int64(0)
				if length == 255 {
					want = 2
				}
				if got := count() - before; got != want {
					t.Fatal("unexpected native entries", got, want)
				}
			})
		}
	}
}
