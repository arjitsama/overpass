package errs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
)

func TestWriteBody(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, http.StatusNotFound, NotFound, "no route /x")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if got["code"] != "not_found" || got["detail"] != "no route /x" || len(got) != 2 {
		t.Fatalf("body = %v", got)
	}
}

func TestErrorString(t *testing.T) {
	if s := New(BadRequest, "bad port").Error(); s != "bad_request: bad port" {
		t.Fatalf("Error() = %q", s)
	}
}

// Every Code constant in errs.go is in the Known registry.
func TestKnownListsEveryCode(t *testing.T) {
	src, err := os.ReadFile("errs.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^\t[A-Z][A-Za-z0-9]*\s+Code = "([^"]+)"`).FindAllSubmatch(src, -1) {
		if !Known(Code(m[1])) {
			t.Errorf("%s is not in the registry", m[1])
		}
	}
	if Known("MADE_UP") {
		t.Error("unknown code accepted")
	}
}
