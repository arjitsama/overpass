// Package wellknown generates every file an agent serves about itself from
// one Config: the signed A2A agent card, the ANS trust card, /health, an
// accessible HTML page and the tier-2 discovery files. Field names follow
// docs/webmesh-spec.md; field values follow the A2A specification. A file
// says only what the agent does: anything unconfigured is left out.
package wellknown

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"time"

	"github.com/arjitsama/overpass/internal/config"
)

// Identity is the agent's ANS identity key and its certificate chain.
type Identity struct {
	Key   *ecdsa.PrivateKey
	Chain []*x509.Certificate // leaf first
	Local bool                // throwaway, generated at startup
}

// LoadIdentity reads the configured key and chain, or generates a throwaway
// self-signed identity for local runs. The leaf must hold the key.
func LoadIdentity(c config.Identity, ansName string) (Identity, error) {
	if c.KeyFile == "" {
		return localIdentity(ansName)
	}
	key, err := readKey(c.KeyFile)
	if err != nil {
		return Identity{}, err
	}
	chain, err := readChain(c.ChainFile)
	if err != nil {
		return Identity{}, err
	}
	leaf, ok := chain[0].PublicKey.(*ecdsa.PublicKey)
	if !ok || !leaf.Equal(&key.PublicKey) {
		return Identity{}, errors.New("identity: leaf certificate does not hold the identity key")
	}
	if now := time.Now(); now.After(chain[0].NotAfter) || now.Before(chain[0].NotBefore) {
		return Identity{}, errors.New("identity: leaf certificate is not currently valid")
	}
	for i := 0; i+1 < len(chain); i++ {
		if err := chain[i].CheckSignatureFrom(chain[i+1]); err != nil {
			return Identity{}, fmt.Errorf("identity: chain[%d] is not signed by chain[%d]: %w", i, i+1, err)
		}
	}
	return Identity{Key: key, Chain: chain}, nil
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identity key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("identity key: no PEM block")
	}
	var key any
	switch block.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, fmt.Errorf("identity key: %w", err)
	}
	k, ok := key.(*ecdsa.PrivateKey)
	if !ok || k.Curve != elliptic.P256() {
		return nil, errors.New("identity key: must be EC P-256")
	}
	return k, nil
}

func readChain(path string) ([]*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identity chain: %w", err)
	}
	var chain []*x509.Certificate
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("identity chain: %w", err)
		}
		chain = append(chain, c)
	}
	if len(chain) == 0 {
		return nil, errors.New("identity chain: no certificates")
	}
	return chain, nil
}

func localIdentity(ansName string) (Identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	u, err := url.Parse(ansName)
	if err != nil {
		return Identity{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: ansName},
		URIs:         []*url.URL{u},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return Identity{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Key: key, Chain: []*x509.Certificate{leaf}, Local: true}, nil
}
