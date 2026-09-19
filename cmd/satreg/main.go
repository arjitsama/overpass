// Command satreg signs the satellite registry that stations load and pin
// (master plan section 3): which authority may issue mandates for each NORAD
// ID, and which Ops agents may hold them.
//
//	satreg sign   -key authority-identity.key -in registry.json -out registry.jws
//	satreg pubkey -key authority-identity.key -out signer.pub.pem
//
// registry.json is a schema.SatRegistry: {"iss", "iat", "satellites": [...]}.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "satreg:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: satreg sign|pubkey -key <pem> [-in <json>] -out <file>")
	}
	fs := flag.NewFlagSet("satreg", flag.ContinueOnError)
	keyPath := fs.String("key", "", "signer's EC P-256 private key (PEM)")
	in := fs.String("in", "", "registry JSON to sign")
	outPath := fs.String("out", "", "output file")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	key, err := readKey(*keyPath)
	if err != nil {
		return err
	}
	switch args[0] {
	case "sign":
		raw, err := os.ReadFile(*in)
		if err != nil {
			return err
		}
		reg, err := decode(raw)
		if err != nil {
			return err
		}
		tok, err := schema.SignSatRegistry(reg, key)
		if err != nil {
			return err
		}
		return write(*outPath, out, []byte(tok+"\n"))
	case "pubkey":
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			return err
		}
		return write(*outPath, out, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func decode(raw []byte) (schema.SatRegistry, error) {
	var r schema.SatRegistry
	err := schema.Decode(raw, &r, errs.SatRegParseError)
	return r, err
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block in key file")
	}
	var k any
	if block.Type == "EC PRIVATE KEY" {
		k, err = x509.ParseECPrivateKey(block.Bytes)
	} else {
		k, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, errors.New("key must be EC P-256")
	}
	return ek, nil
}

func write(path string, out io.Writer, b []byte) error {
	if path == "" {
		_, err := out.Write(b)
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
