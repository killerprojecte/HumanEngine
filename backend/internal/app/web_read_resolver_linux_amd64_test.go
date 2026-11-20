//go:build web_read_candidate && cgo

package app

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
	"golang.org/x/sys/unix"
)

// Re-exec the app test binary through exactly the shipped child entrypoint.
// Only the parent runs the real installed application fixture.
func TestMain(m *testing.M) {
	if child, err := pinnedhop.RunResolverChild(os.Args[1:]); child {
		if err != nil {
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestInstalledWebReadResolverChildLifecycle(t *testing.T) {
	dir := t.TempDir()
	shim, events := filepath.Join(dir, "shim.so"), filepath.Join(dir, "events")
	source := "../pinnedhop/testdata/resolver-child-shim.c"
	if out, err := exec.Command("cc", "-shared", "-fPIC", "-o", shim, source).CombinedOutput(); err != nil {
		t.Fatalf("build isolated libc shim: %v\n%s", err, out)
	}
	t.Setenv("LD_PRELOAD", shim)
	t.Setenv("HE_RESOLVER_CHILD_EVENTS", events)
	assertInstalledWebReadLifecycle(t, func(a *Application, disable func()) {
		setURL := func(value string) {
			appRequest(t, a, http.MethodPost, "/api/plugins/web-model/actions/web-fixture-url", map[string]any{"url": value})
		}
		if _, err := os.Stat(events); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("registration or private literal spawned resolver: %v", err)
		}
		setURL("http://stall.fixture.invalid/")
		appRequest(t, a, http.MethodPost, "/api/plugins/web-model/actions/new-source", map[string]any{})
		until := time.NewTimer(15 * time.Second)
		defer until.Stop()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		var pid int
		for pid == 0 {
			switch data, err := os.ReadFile(events); {
			case err == nil && len(strings.Fields(string(data))) != 0:
				record := strings.Fields(string(data))
				if len(record) != 1 {
					t.Fatalf("expected exactly one helper, got %v", record)
				}
				pid, err = strconv.Atoi(record[0])
				if err != nil || pid < 1 {
					t.Fatalf("invalid synthetic child PID: %d, %v", pid, err)
				}
			case err != nil && !errors.Is(err, os.ErrNotExist):
				t.Fatal(err)
			}
			if pid != 0 {
				break
			}
			select {
			case <-tick.C:
			case <-until.C:
				t.Fatal("real installed WebRead never entered the hold point")
			}
		}
		if err := unix.Kill(pid, 0); err != nil {
			t.Fatalf("resolver not alive before disable: %v", err)
		}
		start := time.Now()
		disable()
		if elapsed := time.Since(start); elapsed >= 4*time.Second {
			t.Errorf("disable did not interrupt held libc before 5s DNS deadline: %s", elapsed)
		}
		if _, err := unix.Wait4(pid, nil, unix.WNOHANG, nil); !errors.Is(err, unix.ECHILD) {
			t.Fatalf("disable returned without reaping exact resolver: %v", err)
		}
		setURL("http://127.0.0.1/")
	})
}
