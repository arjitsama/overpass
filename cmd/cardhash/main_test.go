package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashesServedCard(t *testing.T) {
	card := []byte(`{"name":"gs","url":"https://gs"}`)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(card) }))
	defer srv.Close()
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-k", srv.URL + "/.well-known/agent-card.json"}, &out); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(card)
	want := hex.EncodeToString(sum[:])
	if !strings.Contains(out.String(), "raw_sha256 "+want) || !strings.Contains(out.String(), "jcs_sha256 "+want) ||
		strings.Contains(out.String(), "note:") {
		t.Fatalf("output %q", out.String())
	}
	// Without -k a self-signed agent is refused.
	if err := run(context.Background(), []string{srv.URL}, &out); err == nil {
		t.Fatal("self-signed cert accepted without -k")
	}
}

func TestNonCanonicalFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "card.json")
	_ = os.WriteFile(p, []byte("{ \"b\": 1, \"a\": 2 }"), 0o600)
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-file", p}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "note: served bytes are not JCS canonical") {
		t.Fatalf("output %q", out.String())
	}
	if err := run(context.Background(), nil, &out); err == nil {
		t.Fatal("no args accepted")
	}
}
