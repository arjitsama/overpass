package a2a

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/agentnameservice/ans-sdk-go/pop"
	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/schema"
)

// Scheme names as they appear in the agent card.
const (
	SchemeDPoP    = "ansDPoP"
	SchemeMandate = "overpassMandate"
)

// DPoPGuard authenticates every A2A call with ans-sdk-go's pop.Middleware:
// a DPoP proof (RFC 9449) in the DPoP header, bound to the caller's ANS
// identity certificate through the X-SCITT-Receipt and X-ANS-Status-Token
// headers. keys are the transparency log's root keys; an empty store rejects
// every caller. trustedHost is this agent's own host:port, so htu is never
// compared against the client-controlled Host header.
func DPoPGuard(keys scitt.KeyLookup, replay pop.ReplayCache, trustedHost string, log *slog.Logger) HTTPGuard {
	mw := pop.Middleware(keys, replay, pop.WithTrustedHosts(trustedHost), pop.WithMiddlewareLogger(log))
	return HTTPGuard{
		Scheme: Scheme{Name: SchemeDPoP, Type: "http", Scheme: "DPoP",
			Description: "Every call needs a DPoP proof (RFC 9449, ans-sdk-go pop profile) in the DPoP header, " +
				"bound to the caller's ANS identity certificate by X-SCITT-Receipt and X-ANS-Status-Token headers. " +
				"The status token must say ACTIVE. No mutual TLS."},
		Wrap: func(next http.Handler) http.Handler { return rewrite401(mw, next) },
	}
}

// rewrite401 runs pop's middleware but replaces its plain-text rejection
// bodies with named errs codes (rule 4). The wrapped handler still writes to
// the real ResponseWriter.
func rewrite401(mw func(http.Handler) http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &rejectWriter{orig: w}
		mw(http.HandlerFunc(func(_ http.ResponseWriter, r2 *http.Request) {
			rw.passed = true
			next.ServeHTTP(w, r2)
		})).ServeHTTP(rw, r)
	})
}

// rejectWriter captures what pop writes when it rejects a request.
type rejectWriter struct {
	orig    http.ResponseWriter
	passed  bool
	written bool
}

func (rw *rejectWriter) Header() http.Header { return rw.orig.Header() }

func (rw *rejectWriter) WriteHeader(status int) {
	if rw.passed || rw.written {
		return
	}
	rw.written = true
	rw.orig.Header().Del("Content-Type")
	rw.orig.Header().Del("X-Content-Type-Options")
	switch status {
	case http.StatusUnauthorized:
		errs.Write(rw.orig, status, errs.CallerRejected, "caller authentication failed: DPoP proof, SCITT receipt or status token")
	case http.StatusRequestEntityTooLarge:
		errs.Write(rw.orig, status, errs.PayloadTooLarge, "request content too large")
	default:
		errs.Write(rw.orig, status, errs.CallerRejected, "caller authentication failed")
	}
}

func (rw *rejectWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusUnauthorized)
	}
	return len(b), nil // body replaced by WriteHeader
}

// MandateGuard requires a mandate on a skill and runs book_pass steps 1-2
// (parse, typ, signature) against the issuing authority's keys. keys returns
// the current trusted authority keys; none means every mandate is rejected.
func MandateGuard(keys func() []*ecdsa.PublicKey) SkillGuard {
	return SkillGuard{
		Scheme: Scheme{Name: SchemeMandate, Type: "mandate",
			Description: "The data part must carry \"mandate\": an overpass-mandate+jws (ES256) signed by the " +
				"satellite's authority, with its key in the authority's trust card. See docs/schemas.md."},
		Check: func(_ context.Context, args json.RawMessage) error {
			var a struct {
				Mandate string `json:"mandate"`
			}
			if err := json.Unmarshal(args, &a); err != nil || a.Mandate == "" {
				return errs.New(errs.MandateParseError, "mandate is missing")
			}
			_, err := schema.VerifyMandate(a.Mandate, keys())
			return err
		},
	}
}
