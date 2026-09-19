// Package errs defines the named rejection codes every Overpass endpoint
// returns, and the JSON body that carries them.
package errs

import (
	"encoding/json"
	"net/http"
)

// Code is a stable, machine-readable rejection reason.
type Code string

// Rejection codes. Later phases add their own; names never change once shipped.
const (
	BadRequest       Code = "bad_request"
	NotFound         Code = "not_found"
	MethodNotAllowed Code = "method_not_allowed"
	PayloadTooLarge  Code = "payload_too_large"
	Unavailable      Code = "unavailable"
	Internal         Code = "internal"
)

// Error is a rejection with a named code. It is also the JSON body.
type Error struct {
	Code   Code   `json:"code"`
	Detail string `json:"detail"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Detail }

// New returns an *Error with the given code and detail.
func New(code Code, detail string) *Error {
	return &Error{Code: code, Detail: detail}
}

// Write sends a rejection body {code, detail} with the given HTTP status.
func Write(w http.ResponseWriter, status int, code Code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Detail: detail})
}
