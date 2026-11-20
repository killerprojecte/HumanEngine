//go:build cgo

package pinnedhop

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPackagedResolverChild(t *testing.T) {
	executable := os.Getenv("HE_RESOLVER_QUALIFICATION_BINARY")
	if executable == "" {
		t.Skip("set HE_RESOLVER_QUALIFICATION_BINARY to the exact shipped-profile executable")
	}
	dir := t.TempDir()
	shim, events := filepath.Join(dir, "shim.so"), filepath.Join(dir, "events")
	if out, err := exec.Command("cc", "-shared", "-fPIC", "-o", shim, "testdata/resolver-child-shim.c").CombinedOutput(); err != nil {
		t.Fatalf("build synthetic libc: %v\n%s", err, out)
	}
	t.Setenv("LD_PRELOAD", shim)
	t.Setenv("HE_RESOLVER_CHILD_EVENTS", events)
	readPIDs := func() []string {
		data, err := os.ReadFile(events)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return strings.Fields(string(data))
	}
	reaped := func(pid string) {
		n, err := strconv.Atoi(pid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := unix.Wait4(n, nil, unix.WNOHANG, nil); !errors.Is(err, unix.ECHILD) {
			t.Errorf("child %d was not reaped by lookup: %v", n, err)
		}
	}
	look := func(ctx context.Context, host string) LookupJob {
		return lookupChild(ctx, executable, host)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := look(ctx, "ordered.fixture.invalid")
	<-before.Drained
	if result := <-before.Result; !errors.Is(result.Err, context.Canceled) || len(readPIDs()) != 0 {
		t.Fatalf("cancelled lookup entered libc: %+v", result)
	}
	normal, err := exec.Command(executable, "--help").CombinedOutput()
	if err != nil || len(readPIDs()) != 0 || !strings.Contains(string(normal), "Usage") {
		t.Fatalf("ordinary invocation entered child mode: %v", err)
	}
	if err := exec.Command(executable, resolverChildArg, "unexpected").Run(); err == nil || len(readPIDs()) != 0 {
		t.Fatalf("extended child argv entered libc: %v", err)
	}
	ordered := look(context.Background(), "ordered.fixture.invalid")
	<-ordered.Drained
	reply := <-ordered.Result
	want := []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("8.8.8.8"),
		netip.MustParseAddr("::ffff:8.8.8.8"), netip.MustParseAddr("8.8.8.8")}
	if reply.Err != nil || !slices.Equal(reply.Addresses, want) {
		t.Fatalf("wanted exact family/order/duplicates, got %+v", reply)
	}
	pids := readPIDs()
	if len(pids) != 1 {
		t.Fatalf("wanted one helper, got %v", pids)
	}
	reaped(pids[0])
	denied := look(context.Background(), "denied.fixture.invalid")
	<-denied.Drained
	if result := <-denied.Result; result.Err == nil || len(result.Addresses) != 0 {
		t.Fatalf("unexpected libc failure result: %+v", result)
	}

	for _, timeout := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if timeout {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		}
		prior := len(readPIDs())
		stalled := look(ctx, "stall.fixture.invalid")
		gate := time.After(3 * time.Second)
		for len(readPIDs()) == prior {
			select {
			case <-gate:
				cancel()
				t.Fatal("child never entered synthetic libc")
			case <-time.After(5 * time.Millisecond):
			}
		}
		select {
		case <-stalled.Drained:
			cancel()
			t.Fatal("live libc stall reported as drained")
		default:
		}
		want := context.DeadlineExceeded
		if !timeout {
			want = context.Canceled
			cancel()
		}
		select {
		case <-stalled.Drained:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("held child did not physically drain")
		}
		cancel()
		if result := <-stalled.Result; !errors.Is(result.Err, want) || len(result.Addresses) != 0 {
			t.Fatalf("wrong cancellation result: %+v", result)
		}
		pids = readPIDs()
		if len(pids) != prior+1 {
			t.Fatalf("wanted one isolated stalled helper, got %v", pids[prior:])
		}
		reaped(pids[prior])
	}
}

func BenchmarkPackagedResolverChild(b *testing.B) {
	executable := os.Getenv("HE_RESOLVER_QUALIFICATION_BINARY")
	if executable == "" {
		b.Skip("set HE_RESOLVER_QUALIFICATION_BINARY")
	}
	b.ReportAllocs()
	for b.Loop() {
		// Numeric ambient libc lookup for process overhead only; no external DNS.
		result := lookupChildSync(context.Background(), executable, "127.0.0.1")
		if result.Err != nil || len(result.Addresses) != 1 {
			b.Fatalf("synthetic numeric lookup: %+v", result)
		}
	}
}
