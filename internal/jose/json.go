// Package jose holds Overpass's signing primitives: strict JSON, JCS
// canonicalization (RFC 8785), ES256 JWS (compact and detached) and RFC 7638
// thumbprints. ECDSA and JWS mechanics come from go-jose; this package adds
// the strictness Overpass needs on top.
package jose

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
)

// MaxSafeInt is the largest integer JCS preserves exactly (2^53-1): RFC 8785
// serializes numbers as IEEE-754 doubles.
const MaxSafeInt = 1<<53 - 1

// maxDepth bounds nesting in strict JSON.
const maxDepth = 32

// StrictJSON accepts exactly one JSON value of at most max bytes, in valid
// UTF-8, whose numbers are all integers within ±MaxSafeInt and whose objects
// have no duplicate keys. Floats in any spelling (100.0, 1e2) are rejected.
func StrictJSON(b []byte, max int) error {
	if len(b) > max {
		return fmt.Errorf("json exceeds %d bytes", max)
	}
	if !utf8.Valid(b) {
		return errors.New("json is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := walk(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after json value")
	}
	return nil
}

func walk(dec *json.Decoder, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("json nested deeper than %d", maxDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("json: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return walkObject(dec, depth)
		case '[':
			return walkArray(dec, depth)
		}
		return fmt.Errorf("json: unexpected %q", t)
	case json.Number:
		return checkInt(t)
	}
	return nil // string, bool, null
}

func walkObject(dec *json.Decoder, depth int) error {
	seen := map[string]struct{}{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("json: %w", err)
		}
		k, _ := tok.(string)
		if _, dup := seen[k]; dup {
			return fmt.Errorf("json: duplicate key %q", k)
		}
		seen[k] = struct{}{}
		if err := walk(dec, depth+1); err != nil {
			return err
		}
	}
	_, err := dec.Token() // '}'
	return err
}

func walkArray(dec *json.Decoder, depth int) error {
	for dec.More() {
		if err := walk(dec, depth+1); err != nil {
			return err
		}
	}
	_, err := dec.Token() // ']'
	return err
}

// checkInt accepts only the integer grammar -?(0|[1-9][0-9]*), without -0.
func checkInt(n json.Number) error {
	s := string(n)
	digits := s
	if len(s) > 0 && s[0] == '-' {
		digits = s[1:]
	}
	if digits == "" || (len(digits) > 1 && digits[0] == '0') || s == "-0" {
		return fmt.Errorf("json: number %q is not a canonical integer", s)
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return fmt.Errorf("json: number %q is not an integer (floats are not allowed)", s)
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v > MaxSafeInt || v < -MaxSafeInt {
		return fmt.Errorf("json: integer %q outside ±2^53-1", s)
	}
	return nil
}

// Canonicalize marshals v and returns its RFC 8785 (JCS) form.
func Canonicalize(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Transform(raw)
}

// Transform returns the JCS form of raw JSON. It never panics.
func Transform(raw []byte) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("jcs: %v", r)
		}
	}()
	return jcs.Transform(raw)
}
