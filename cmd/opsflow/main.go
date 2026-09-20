// Command opsflow runs one real pass from Mission Ops across separate agent
// processes: verify the station, quote, get a mandate from the authority over
// A2A, book, and relay one command. It is the runnable form of internal/opsflow
// used by scripts/accept/phase-12.sh. Read/act as Ops; it books a real pass.
//
//	opsflow -config ops.yaml -station-host gs-blacksburg.localhost \
//	  -station-url https://gs-blacksburg.localhost:8444/ \
//	  -authority-url https://authority.localhost:8445/ \
//	  -authority-key certs/authority/identity.pub -norad 27844 -mode uplink [-insecure]
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/opsflow"
	"github.com/arjitsama/overpass/internal/station"
	"github.com/arjitsama/overpass/internal/verify"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "opsflow:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("opsflow", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "Ops agent config (required)")
	stationHost := fs.String("station-host", "", "station host to verify (required)")
	stationURL := fs.String("station-url", "", "station A2A URL (required)")
	authorityURL := fs.String("authority-url", "", "authority A2A URL (required)")
	authorityKey := fs.String("authority-key", "", "authority identity public key PEM (to read the mandate id)")
	norad := fs.Int64("norad", 27844, "NORAD id")
	mode := fs.String("mode", "uplink", "uplink|downlink")
	lead := fs.Duration("lead", time.Hour, "how far ahead the pass window starts")
	dur := fs.Duration("dur", 8*time.Minute, "pass duration")
	maxElev := fs.Int64("max-elev", 45, "max elevation degrees")
	insecure := fs.Bool("insecure", false, "skip TLS verification (local self-signed agents only)")
	caFile := fs.String("ca", "", "PEM of CAs/certs to trust")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, v := range map[string]string{"-config": *cfgPath, "-station-host": *stationHost,
		"-station-url": *stationURL, "-authority-url": *authorityURL} {
		if v == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	v, err := verify.New(cfg, verify.Options{Self: cfg.Host})
	if err != nil {
		return err
	}
	key, certDER, err := loadIdentity(cfg)
	if err != nil {
		return err
	}
	env := cfg.Session.OpsEnv
	if env == "" {
		env = config.ProdEnv
	}
	out, sup, err := v.NewOutbound(key, certDER, env, cfg.Card.AgentID)
	if err != nil {
		return err
	}
	if err := sup.RefreshNow(ctx); err != nil {
		return fmt.Errorf("fetch SCITT headers: %w", err)
	}
	defer sup.StartAutoRefresh(ctx)()

	hc, err := httpClient(*insecure, *caFile)
	if err != nil {
		return err
	}
	var authKeys []*ecdsa.PublicKey
	if *authorityKey != "" {
		pub, err := station.LoadPublicKey(*authorityKey)
		if err != nil {
			return err
		}
		authKeys = []*ecdsa.PublicKey{pub}
	}

	now := time.Now()
	f := &opsflow.Flow{Peers: v, Sign: out, HTTP: hc, OpsANS: cfg.Host, CommandKey: key, AuthKeys: authKeys, Now: time.Now}
	res, err := f.Run(ctx, opsflow.Target{
		StationHost: *stationHost, StationURL: *stationURL, AuthorityURL: *authorityURL,
		NoradID: *norad, Mode: *mode, AOS: now.Add(*lead).Unix(), LOS: now.Add(*lead + *dur).Unix(),
		MaxElevDeg: *maxElev,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Printf("PASS station=%s quote=%s mandate=%s booking=%s ack=%s\n",
		res.StationANS, res.QuoteID, res.MandateID, res.BookingID, res.Ack.Result)
	return nil
}

func loadIdentity(cfg config.Config) (*ecdsa.PrivateKey, []byte, error) {
	raw, err := os.ReadFile(cfg.Identity.KeyFile)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, nil, fmt.Errorf("%s: no PEM block", cfg.Identity.KeyFile)
	}
	var key *ecdsa.PrivateKey
	if block.Type == "EC PRIVATE KEY" {
		key, err = x509.ParseECPrivateKey(block.Bytes)
	} else {
		var k any
		k, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			if key, ok = k.(*ecdsa.PrivateKey); !ok {
				return nil, nil, fmt.Errorf("%s: not an EC key", cfg.Identity.KeyFile)
			}
		}
	}
	if err != nil {
		return nil, nil, err
	}
	chain, err := os.ReadFile(cfg.Identity.ChainFile)
	if err != nil {
		return nil, nil, err
	}
	cb, _ := pem.Decode(chain)
	if cb == nil {
		return nil, nil, fmt.Errorf("%s: no certificate", cfg.Identity.ChainFile)
	}
	return key, cb.Bytes, nil
}

func httpClient(insecure bool, caFile string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tlsc := &tls.Config{MinVersion: tls.VersionTLS12}
	if insecure {
		tlsc.InsecureSkipVerify = true
	}
	if caFile != "" {
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("%s: no certificates", caFile)
		}
		tlsc.RootCAs = pool
	}
	tr.TLSClientConfig = tlsc
	return &http.Client{Timeout: 15 * time.Second, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
