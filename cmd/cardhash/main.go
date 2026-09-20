// Command cardhash prints the SHA-256 of an agent card, for comparison with
// the metaDataHash registered on ANS. The registry's exact hashing rule is not
// documented in ans-sdk-go, so it prints both the hash of the raw served bytes
// and the hash of its JCS (RFC 8785) form. Overpass serves cards in JCS form,
// so for our agents the two are equal.
//
//	cardhash https://gs-blacksburg.example/.well-known/agent-card.json
//	cardhash -k https://localhost:8444/.well-known/agent-card.json   (self-signed, local only)
//	cardhash -file card.json
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/arjitsama/overpass/internal/jose"
)

const maxCardBytes = 1 << 20

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "cardhash:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("cardhash", flag.ContinueOnError)
	file := fs.String("file", "", "read the card from a file instead of a URL")
	insecure := fs.Bool("k", false, "skip TLS verification (local self-signed agents only)")
	caFile := fs.String("ca", "", "PEM bundle of extra root CAs to trust (e.g. a root the OS store lacks)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var raw []byte
	var err error
	switch {
	case *file != "":
		raw, err = readFile(*file)
	case fs.NArg() == 1:
		raw, err = fetch(ctx, fs.Arg(0), *insecure, *caFile)
	default:
		return errors.New("usage: cardhash [-k] <card URL> | cardhash -file <path>")
	}
	if err != nil {
		return err
	}
	rawSum := sha256.Sum256(raw)
	fmt.Fprintf(out, "raw_sha256 %s\n", hex.EncodeToString(rawSum[:]))
	canon, err := jose.Transform(raw)
	if err != nil {
		return fmt.Errorf("card is not JSON: %w", err)
	}
	jcsSum := sha256.Sum256(canon)
	fmt.Fprintf(out, "jcs_sha256 %s\n", hex.EncodeToString(jcsSum[:]))
	if rawSum != jcsSum {
		fmt.Fprintln(out, "note: served bytes are not JCS canonical; the two hashes differ")
	}
	return nil
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readCapped(f)
}

func fetch(ctx context.Context, url string, insecure bool, caFile string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecure, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in -k for local self-signed agents
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("-ca: %w", err)
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("-ca: no certificates in " + caFile)
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return readCapped(resp.Body)
}

func readCapped(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxCardBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxCardBytes {
		return nil, fmt.Errorf("card exceeds %d bytes", maxCardBytes)
	}
	return raw, nil
}
