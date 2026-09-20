// Package web serves the Overpass dashboard: three static files (index.html,
// app.js, styles.css) with no framework, no build step, and no external
// requests (master plan §12). The Ops agent mounts Handler() under /ui/.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed index.html app.js styles.css
var assets embed.FS

//go:embed testdata/demo-events.json
var demoEvents []byte

// DemoEventsJSON is the recorded demo event stream (the same file the DOM test
// replays), used by the Ops "Run demo pass" route to drive the dashboard from
// recorded data. It is demo scaffolding, not a live pass.
func DemoEventsJSON() []byte { return demoEvents }

// contentType maps the three known extensions; everything else is 404'd rather
// than served with a guessed type.
func contentType(path string) string {
	switch {
	case strings.HasSuffix(path, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(path, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(path, ".css"):
		return "text/css; charset=utf-8"
	default:
		return ""
	}
}

// Handler serves the dashboard. GET (and HEAD) "" or "/" return index.html;
// "app.js" and "styles.css" return those. The mount point supplies the prefix,
// so paths here are relative. Other paths and methods are refused.
func Handler(notFound http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		ct := contentType(name)
		if ct == "" {
			notFound.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := fs.ReadFile(assets, name)
		if err != nil {
			notFound.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(body)
	})
}

// FS exposes the embedded assets for tests (contrast, HTML structure).
func FS() fs.FS { return assets }
