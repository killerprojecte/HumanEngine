//go:build web_read_candidate && cgo && linux

package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOSDNSNativeBoundary(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Fatal("native boundary probe requires the existing C compiler", err)
	}
	shim := filepath.Join(t.TempDir(), "dns-native-shim.so")
	if output, err := exec.Command(cc, "-std=c11", "-shared", "-fPIC", "-o", shim, "testdata/dns-native-shim.c", "-ldl").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, output)
	}
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOSDNSNativeBoundaryChild$", "-test.v")
			// Interposition is limited to this synthetic subprocess, not the application.
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "LD_PRELOAD=") && !strings.HasPrefix(entry, "HE_DNS_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "LD_PRELOAD="+shim, "HE_DNS_CHILD=1")
			if failure {
				cmd.Env = append(cmd.Env, "HE_DNS_FAIL=1")
			}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("native probe: %v %s", err, output)
			} else {
				t.Log(string(output))
			}
		})
	}
}

func TestOSDNSNativeBoundaryChild(t *testing.T) {
	if os.Getenv("HE_DNS_CHILD") != "1" {
		t.Skip("isolated native interposition child only")
	}
	eventsR, eventsW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer eventsR.Close()
	defer eventsW.Close()
	gateR, gateW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gateR.Close()
	defer gateW.Close()
	t.Setenv("HE_DNS_EVENTS", fmt.Sprint(eventsW.Fd()))
	t.Setenv("HE_DNS_GATE", fmt.Sprint(gateR.Fd()))
	phase := func(want byte) {
		t.Helper()
		var b [1]byte
		if _, err := eventsR.Read(b[:]); err != nil || b[0] != want {
			t.Fatalf("native phase %q, want %q: %v", b, want, err)
		}
	}
	release := func() {
		t.Helper()
		if _, err := gateW.Write([]byte{'R'}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewOSResolver()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := r.Lookup(ctx, "127.0.0.1")
	phase('E')
	cancel()
	r.Close()
	joined := make(chan struct{})
	go func() { r.Wait(); close(joined) }()
	assertHeld := func() {
		t.Helper()
		select {
		case <-job.Drained:
			t.Fatal("native call falsely drained")
		default:
		}
		select {
		case <-job.Result:
			t.Fatal("native result escaped early")
		default:
		}
		select {
		case <-joined:
			t.Fatal("shutdown passed native call")
		default:
		}
	}
	assertHeld()
	release()
	if os.Getenv("HE_DNS_FAIL") == "" {
		phase('F')
		assertHeld()
		release()
	}
	requireDNSDrain(t, job)
	<-joined // no Result consumer was needed for either native or owner completion
	answer := <-job.Result
	if os.Getenv("HE_DNS_FAIL") != "" {
		if answer.Err == nil {
			t.Fatal("missing native failure")
		}
	} else if answer.Err != nil || len(answer.Addresses) != 1 || answer.Addresses[0].String() != "127.0.0.1" {
		t.Fatal(answer)
	}
}
