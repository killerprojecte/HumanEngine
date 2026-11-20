package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

type previewUI struct{ root *os.Root }

func openPreviewUI(directory string) (*previewUI, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("runtime_preview_ui_root_invalid")
	}
	for _, name := range [...]string{"index.html", "200.html"} {
		info, err := root.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			_ = root.Close()
			return nil, errors.New("runtime_preview_ui_shell_missing")
		}
	}
	return &previewUI{root: root}, nil
}

// API traffic retains the original request, transport and application owner.
// The static branch never authenticates, proxies or interprets API requests.
func (ui *previewUI) handler(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/api" || strings.HasPrefix(p, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(p, "/")
		switch {
		case p == "/":
			name = "index.html"
		case !fs.ValidPath(strings.TrimSuffix(name, "/")):
			http.NotFound(w, r)
			return
		case p != "/_nuxt" && !strings.HasPrefix(p, "/_nuxt/") &&
			path.Ext(strings.TrimSuffix(p, "/")) == "" && strings.Contains(r.Header.Get("Accept"), "text/html"):
			name = "200.html"
		}
		file, err := ui.root.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		cache := "no-cache"
		if strings.HasPrefix(name, "_nuxt/") && !strings.HasPrefix(name, "_nuxt/builds/") {
			cache = "public, max-age=31536000, immutable"
		}
		w.Header().Set("Cache-Control", cache)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, info.ModTime(), file)
	})
}
