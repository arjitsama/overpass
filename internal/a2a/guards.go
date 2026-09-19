package a2a

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/agentnameservice/ans-sdk-go/pop"
	"github.com/agentnameservice/ans-sdk-go/verify/scitt"

	"github.com/arjitsama/overpass/internal/errs"
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
// compared against the client-controlled Host header. replay records proof
// jtis (book_pass check 10); a replayed proof is DPOP_REJECTED:replay.
func DPoPGuard(keys scitt.KeyLookup, replay pop.ReplayCache, trustedHost string, log *slog.Logger) HTTPGuard {
	// Probe once at construction so wiring mistakes fail at startup.
	_ = pop.Middleware(keys, replay, pop.WithTrustedHosts(trustedHost), pop.WithMiddlewareLogger(log))
	// Per request, wrap the cache to learn whether a rejection was a replay:
	// the SDK answers every failure with the same 401.
	mw := func(seen *bool) func(http.Handler) http.Handler {
		return pop.Middleware(keys, &flagReplay{inner: replay, seen: seen},
			pop.WithTrustedHosts(trustedHost), pop.WithMiddlewareLogger(log))
	}
	return HTTPGuard{
		Scheme: Scheme{Name: SchemeDPoP, Type: "http", Scheme: "DPoP",
			Description: "Every call needs a DPoP proof (RFC 9449, ans-sdk-go pop profile) in the DPoP header, " +
				"bound to the caller's ANS identity certificate by X-SCITT-Receipt and X-ANS-Status-Token headers. " +
				"The status token must say ACTIVE. No mutual TLS."},
		Wrap: func(next http.Handler) http.Handler { return rewrite401(mw, next) },
	}
}

// flagReplay notes when the SDK saw a replayed jti in this request.
type flagReplay struct {
	inner pop.ReplayCache
	seen  *bool
}

func (f *flagReplay) CheckAndStore(key string, exp time.Time) (bool, error) {
	seen, err := f.inner.CheckAndStore(key, exp)
	if seen {
		*f.seen = true
	}
	return seen, err
}

// rewrite401 runs pop's middleware but replaces its plain-text rejection
// bodies with named errs codes (rule 4). The wrapped handler still writes to
// the real ResponseWriter.
func rewrite401(mw func(seen *bool) func(http.Handler) http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &rejectWriter{orig: w}
		mw(&rw.replay)(http.HandlerFunc(func(_ http.ResponseWriter, r2 *http.Request) {
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
	replay  bool // the proof's jti was already seen
}

func (rw *rejectWriter) Header() http.Header { return rw.orig.Header() }

func (rw *rejectWriter) WriteHeader(status int) {
	if rw.passed || rw.written {
		return
	}
	rw.written = true
	rw.orig.Header().Del("Content-Type")
	rw.orig.Header().Del("X-Content-Type-Options")
	switch {
	case status == http.StatusUnauthorized && rw.replay:
		errs.Write(rw.orig, status, errs.DPoPRejectedReplay, "DPoP proof replayed (jti already seen)")
	case status == http.StatusUnauthorized:
		errs.Write(rw.orig, status, errs.CallerRejected, "caller authentication failed: DPoP proof, SCITT receipt or status token")
	case status == http.StatusRequestEntityTooLarge:
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

// MandateDeclared declares the mandate scheme on a skill whose handler
// enforces it itself: book_pass runs its twelve checks in one fixed order
// (master plan 8.5), so the mandate is not checked ahead of it here.
func MandateDeclared() SkillGuard {
	return SkillGuard{Scheme: Scheme{Name: SchemeMandate, Type: "mandate",
		Description: "The data part must carry \"mandate\": an overpass-mandate+jws (ES256) signed by the " +
			"satellite's authority, with its key in the authority's trust card, plus \"quote_id\". The DPoP key " +
			"must be the mandate's jkt. See docs/schemas.md."}}
}
