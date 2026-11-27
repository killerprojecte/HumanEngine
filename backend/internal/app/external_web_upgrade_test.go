//go:build linux && sqlite_fts5 && humanengine_icu

package app

import (
	"context"
	"crypto/sha256"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
)

func TestExternalWebReadColdUpgradeRollback(t *testing.T) {
	page, request := os.Getenv("HE_PAGE_PACKAGE"), os.Getenv("HE_REQUEST_PACKAGE")
	if page == "" || request == "" {
		t.Skip("explicit independent GET and request packages required")
	}
	const entry = "web-read.v2.json"
	const phaseKey = "HE_WEB_READ_COLD_PHASE"
	const rootKey = "HE_WEB_READ_COLD_DATA"
	if phase := os.Getenv(phaseKey); phase != "" {
		runWebReadColdPhase(t, phase, os.Getenv(rootKey), page, request, entry)
		return
	}
	digest := func(pkg, name string) [32]byte {
		data, err := os.ReadFile(filepath.Join(pkg, name))
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(data)
	}
	pageManifest, requestManifest := digest(page, entry), digest(request, entry)
	wasm := digest(page, "web-read.wasm")
	if pageManifest == requestManifest || wasm != digest(request, "web-read.wasm") {
		t.Fatal("fixture must change the manifest, not the guest binary")
	}
	t.Logf("package-manifests get=%x request=%x shared-wasm=%x", pageManifest, requestManifest, wasm)

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"get", "upgrade", "rollback"} {
		if !t.Run(phase, func(t *testing.T) {
			cmd := exec.CommandContext(ctx, bin, "-test.v", "-test.run=^TestExternalWebReadColdUpgradeRollback$")
			cmd.Env = append(os.Environ(), phaseKey+"="+phase, rootKey+"="+root)
			output, err := cmd.CombinedOutput()
			t.Logf("%s", output)
			if err != nil {
				t.Fatal(err)
			}
		}) {
			t.FailNow()
		}
	}
	t.Log("cold-get-request-get-settings-and-intent-preserved")
}

func runWebReadColdPhase(t *testing.T, phase, root, page, request, entry string) {
	t.Helper()
	const id = "web-read"
	const settings = "/api/plugins/web-read/config"
	ctx := t.Context()
	pkg, origin, run := page, os.Getenv("HE_PAGE_ORIGIN"), runPublicPagePlanner
	if phase == "upgrade" {
		pkg, origin, run = request, os.Getenv("HE_REQUEST_ORIGIN"), runStructuredHTTPPlanner
	}
	initial := phase == "get"
	a, err := Open(ctx, Options{
		Installation: installation.Options{
			Database: filepath.Join(root, "live/main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"),
			VectorExtension: filepath.Join(os.Getenv("HE_SOURCE_ROOT"), "node_modules/sqlite-vec-linux-x64/vec0.so"), SourceTimezone: "UTC",
		},
		PluginRoot: pkg, ImageLibrary: os.Getenv("HE_IMAGE_LIBRARY"), Node: "node", AdminToken: "synthetic-token", Locale: "en-US",
		Entries: []string{entry}, Builtins: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	catalog := appRequest(t, a, http.MethodGet, "/api/plugins", nil).([]any)
	if len(catalog) != 1 || field(catalog[0], "id") != id || field(catalog[0], "builtin") != false ||
		field(catalog[0], "enabled") != (!initial) {
		t.Fatal("package identity or durable enablement changed", catalog)
	}
	if initial {
		appRequest(t, a, http.MethodPut, settings, map[string]any{"maxChars": 256, "timeoutMs": 5400})
		appRequest(t, a, http.MethodPut, "/api/plugins/web-read/enabled", map[string]any{"enabled": true})
	}
	cfg := appRequest(t, a, http.MethodGet, settings, nil)
	if field(cfg, "maxChars") != float64(256) || field(cfg, "timeoutMs") != float64(5400) {
		t.Fatal("package settings changed", cfg)
	}
	if !a.WASM.Loaded(id) || a.Bridge.Host.PID() != 0 {
		t.Fatal("expected restored WASM without Node")
	}
	run(t, ctx, a, "web-"+phase, origin)
}
