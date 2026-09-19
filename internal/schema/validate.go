package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Field limits.
const (
	maxIDLen      = 128
	maxNameLen    = 253
	maxClasses    = 32
	maxClassLen   = 32
	maxSatellites = 256
	maxOpsPerSat  = 16
	maxChecks     = 64
	maxBodyBytes  = 16 << 10
	maxNorad      = 999_999_999
)

// Modes and results.
const (
	ModeUplink   = "uplink"
	ModeDownlink = "downlink"
)

func checkMode(m string) error {
	if m != ModeUplink && m != ModeDownlink {
		return fmt.Errorf("mode %q must be uplink or downlink", m)
	}
	return nil
}

// checkID: 1-128 chars of [A-Za-z0-9._:-].
func checkID(field, s string) error {
	if s == "" || len(s) > maxIDLen {
		return fmt.Errorf("%s must be 1-%d characters", field, maxIDLen)
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return fmt.Errorf("%s has invalid character %q", field, c)
		}
	}
	return nil
}

// checkName: an ANS name or host, 1-253 printable ASCII without spaces.
// Exact ANS name grammar is checked by the verifier in Phase 3.
func checkName(field, s string) error {
	if s == "" || len(s) > maxNameLen {
		return fmt.Errorf("%s must be 1-%d characters", field, maxNameLen)
	}
	for _, c := range s {
		if c <= ' ' || c > '~' {
			return fmt.Errorf("%s has invalid character %q", field, c)
		}
	}
	return nil
}

func checkPositive(field string, v int64) error {
	if v <= 0 {
		return fmt.Errorf("%s must be positive", field)
	}
	return nil
}

func checkNorad(v int64) error {
	if v <= 0 || v > maxNorad {
		return fmt.Errorf("norad_id %d out of range", v)
	}
	return nil
}

func checkWindow(startField string, start int64, endField string, end int64) error {
	if err := checkPositive(startField, start); err != nil {
		return err
	}
	if end <= start {
		return fmt.Errorf("%s must be after %s", endField, startField)
	}
	return nil
}

// checkHex64: lowercase hex SHA-256.
func checkHex64(field, s string) error {
	if len(s) != 64 {
		return fmt.Errorf("%s must be 64 lowercase hex characters", field)
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("%s must be 64 lowercase hex characters", field)
		}
	}
	return nil
}

// checkB64URL: base64url without padding, length between min and max.
func checkB64URL(field, s string, min, max int) error {
	if len(s) < min || len(s) > max {
		return fmt.Errorf("%s must be %d-%d base64url characters", field, min, max)
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return fmt.Errorf("%s must be base64url", field)
		}
	}
	return nil
}

// checkClass: command class, [a-z0-9_-]{1,32}.
func checkClass(c string) error {
	if c == "" || len(c) > maxClassLen {
		return fmt.Errorf("command class must be 1-%d characters", maxClassLen)
	}
	for _, r := range c {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return fmt.Errorf("command class %q has invalid character %q", c, r)
		}
	}
	return nil
}

func checkClasses(cs []string) error {
	if len(cs) == 0 || len(cs) > maxClasses {
		return fmt.Errorf("command_classes must have 1-%d entries", maxClasses)
	}
	seen := map[string]bool{}
	for _, c := range cs {
		if err := checkClass(c); err != nil {
			return err
		}
		if seen[c] {
			return fmt.Errorf("duplicate command class %q", c)
		}
		seen[c] = true
	}
	return nil
}

func checkOneOf(field, v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("%s %q must be one of %v", field, v, allowed)
}

// checkBody: a JSON object (the command body), at most maxBodyBytes.
func checkBody(b json.RawMessage) error {
	if len(b) == 0 || len(b) > maxBodyBytes {
		return fmt.Errorf("body must be 1-%d bytes", maxBodyBytes)
	}
	if b[0] != '{' {
		return errors.New("body must be a JSON object")
	}
	return nil
}

// Scope builds a mandate scope "pass:<mode>:<norad_id>".
func Scope(mode string, noradID int64) string {
	return "pass:" + mode + ":" + strconv.FormatInt(noradID, 10)
}

// ParseScope splits a scope into mode and NORAD ID.
func ParseScope(s string) (mode string, noradID int64, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] != "pass" {
		return "", 0, fmt.Errorf("scope %q must be pass:<mode>:<norad_id>", s)
	}
	if err := checkMode(parts[1]); err != nil {
		return "", 0, err
	}
	n, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || Scope(parts[1], n) != s {
		return "", 0, fmt.Errorf("scope %q has a malformed norad_id", s)
	}
	if err := checkNorad(n); err != nil {
		return "", 0, err
	}
	return parts[1], n, nil
}
