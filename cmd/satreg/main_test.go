package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/station"
)

func TestSignThenStationLoads(t *testing.T) {
	dir := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPath := filepath.Join(dir, "k.pem")
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	in := filepath.Join(dir, "reg.json")
	_ = os.WriteFile(in, []byte(`{"iss":"ans://v0.1.0.authority.localhost","iat":1790000000,"satellites":[{"norad_id":27844,"authority_ans_name":"ans://v0.1.0.authority.localhost","ops_ans_names":["ans://v0.1.0.ops.localhost"]}]}`), 0o600)
	jws, pub := filepath.Join(dir, "reg.jws"), filepath.Join(dir, "pub.pem")
	if err := run([]string{"sign", "-key", keyPath, "-in", in, "-out", jws}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"pubkey", "-key", keyPath, "-out", pub}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	reg, err := station.LoadRegistry(config.SatReg{File: jws, SignerKeyFile: pub})
	if err != nil || reg.Satellites[0].NoradID != 27844 {
		t.Fatalf("%+v %v", reg, err)
	}
	// Pinning: another signer's key does not load it.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	oder, _ := x509.MarshalPKIXPublicKey(&other.PublicKey)
	_ = os.WriteFile(pub, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: oder}), 0o600)
	if _, err := station.LoadRegistry(config.SatReg{File: jws, SignerKeyFile: pub}); err == nil {
		t.Fatal("registry loaded under a different signer")
	}
	for _, bad := range [][]string{{}, {"sign", "-key", keyPath, "-in", filepath.Join(dir, "missing.json")}, {"nope", "-key", keyPath}} {
		if run(bad, &bytes.Buffer{}) == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
