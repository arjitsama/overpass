// Package schema defines every Overpass wire object. docs/schemas.md is the
// human-readable form of this package and is frozen after Phase 1.
//
// Decoding is strict: one JSON value, integers only (no floats in any
// spelling), no duplicate or unknown keys, exact key spelling, every field
// present unless marked optional, and a size cap. Signed payloads must also be
// byte-for-byte JCS canonical.
package schema

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/jose"
)

// MaxObjectBytes caps any encoded object.
const MaxObjectBytes = 48 << 10

// Object is any wire object.
type Object interface {
	Validate() error
}

// Decode strictly decodes raw into v and validates it; failures carry code.
func Decode(raw []byte, v Object, code errs.Code) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errs.New(code, "malformed object")
		}
	}()
	if err := jose.StrictJSON(raw, MaxObjectBytes); err != nil {
		return errs.New(code, err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errs.New(code, err.Error())
	}
	// encoding/json matches keys case-insensitively and fills absent fields
	// with zero values; re-encoding and comparing catches both.
	canon, err := jose.Canonicalize(v)
	if err != nil {
		return errs.New(code, err.Error())
	}
	given, err := jose.Transform(raw)
	if err != nil || !bytes.Equal(given, canon) {
		return errs.New(code, "fields do not match the schema exactly (missing, misspelled or extra)")
	}
	if err := v.Validate(); err != nil {
		return errs.New(code, err.Error())
	}
	return nil
}

// decodeSigned is Decode plus: the payload must already be JCS canonical, so
// the signature covers exactly one encoding.
func decodeSigned(raw []byte, v Object, code errs.Code) error {
	if err := Decode(raw, v, code); err != nil {
		return err
	}
	canon, _ := jose.Canonicalize(v)
	if !bytes.Equal(raw, canon) {
		return errs.New(code, "signed payload is not JCS canonical")
	}
	return nil
}

// Profiles for each signed object.
var (
	MandateProfile = jose.Profile{Typ: jose.TypMandate, ParseCode: errs.MandateParseError, SigCode: errs.MandateRejectedSignature}
	SatRegProfile  = jose.Profile{Typ: jose.TypSatReg, ParseCode: errs.SatRegParseError, SigCode: errs.SatRegRejectedSignature}
	CommandProfile = jose.Profile{Typ: jose.TypCommand, ParseCode: errs.CommandParseError, SigCode: errs.CommandRejectedSignature}
	AuditProfile   = jose.Profile{Typ: jose.TypAudit, ParseCode: errs.AuditParseError, SigCode: errs.AuditRejectedSignature}
	ReceiptProfile = jose.Profile{Typ: jose.TypReceipt, ParseCode: errs.ReceiptParseError, SigCode: errs.ReceiptRejectedSignature}
	CardProfile    = jose.Profile{Typ: jose.TypAgentCard, ParseCode: errs.CardParseError, SigCode: errs.CardRejectedSignature, AllowJKU: true}
)

// sign validates v, canonicalizes it and signs it as a compact JWS.
func sign(p jose.Profile, v Object, key *ecdsa.PrivateKey) (string, error) {
	if err := v.Validate(); err != nil {
		return "", fmt.Errorf("refusing to sign invalid %s: %w", p.Typ, err)
	}
	payload, err := jose.Canonicalize(v)
	if err != nil {
		return "", err
	}
	return jose.Sign(p.Typ, payload, key, "")
}

// verify checks token against p and keys and decodes the payload into v.
func verify(token string, p jose.Profile, keys []*ecdsa.PublicKey, v Object) error {
	_, err := jose.Verify(token, p, keys, func(b []byte) error {
		return decodeSigned(b, v, p.ParseCode)
	})
	return err
}
