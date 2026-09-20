package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesAssets(t *testing.T) {
	notFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h := http.StripPrefix("/ui", Handler(notFound))

	cases := []struct {
		path, ctype, want string
	}{
		{"/ui/", "text/html; charset=utf-8", "<h1>Overpass</h1>"},
		{"/ui/index.html", "text/html; charset=utf-8", "Skip to pass schedule"},
		{"/ui/app.js", "text/javascript; charset=utf-8", "applyEvent"},
		{"/ui/styles.css", "text/css; charset=utf-8", "--accent-pass"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", c.path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != c.ctype {
			t.Errorf("%s: content-type %q, want %q", c.path, got, c.ctype)
		}
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: body missing %q", c.path, c.want)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", c.path)
		}
	}

	// Unknown path falls through to notFound; a non-GET is refused.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/secrets.txt", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown path: status %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ui/app.js", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST asset: status %d, want 405", rec.Code)
	}
}
