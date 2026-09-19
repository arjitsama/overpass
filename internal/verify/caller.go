package verify

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"net/http"

	"github.com/agentnameservice/ans-sdk-go/pop"
	"github.com/agentnameservice/ans-sdk-go/verify/scitt"
)

// Caller returns the proven caller identity that a2a.DPoPGuard (built on
// pop.Middleware) put in the request context. Authorization is separate
// from identity (master plan 6.8): the caller's handler decides what this
// identity may do.
func Caller(ctx context.Context) (*pop.CallerIdentity, bool) {
	return pop.CallerFromContext(ctx)
}

// Outbound signs this agent's requests: a fresh DPoP proof from its
// identity key plus its own SCITT receipt and status token headers, so a
// callee's pop.Middleware can authenticate it without mutual TLS.
type Outbound struct {
	signer  *pop.Signer
	headers func() (http.Header, error)
}

// NewOutbound builds the signer from the identity key and certificate and
// supplies SCITT headers for agentID from the named environment's log. Call
// Start to fetch and keep them fresh.
func (v *Verifier) NewOutbound(key *ecdsa.PrivateKey, certDER []byte, envName, agentID string) (*Outbound, *scitt.HeaderSupplier, error) {
	e, ok := v.envs[envName]
	if !ok {
		return nil, nil, fmt.Errorf("unknown environment %q", envName)
	}
	signer, err := pop.NewSigner(key, certDER)
	if err != nil {
		return nil, nil, err
	}
	sup := scitt.NewHeaderSupplierWithStaticKeys(agentID, e.scitt, e.keys)
	return &Outbound{signer: signer, headers: func() (http.Header, error) {
		h := sup.CurrentHeaders()
		if h == nil || h.IsEmpty() {
			return nil, errors.New("no SCITT headers yet: the supplier has not fetched a receipt and status token")
		}
		return h.ToHTTPHeaders()
	}}, sup, nil
}

// NewOutboundStatic signs with fixed receipt and status token bytes (tests,
// and local runs where the headers come from files).
func NewOutboundStatic(key *ecdsa.PrivateKey, certDER, receipt, statusToken []byte) (*Outbound, error) {
	signer, err := pop.NewSigner(key, certDER)
	if err != nil {
		return nil, err
	}
	h := scitt.GenerateHeaders(receipt, statusToken)
	return &Outbound{signer: signer, headers: func() (http.Header, error) { return h, nil }}, nil
}

// JKT is the RFC 7638 thumbprint of the DPoP key, for a mandate's jkt.
func (o *Outbound) JKT() string { return o.signer.JKT() }

// Attach adds the DPoP proof and SCITT headers to req. The body, if any, is
// bound by digest (pop.AttachIdentity).
func (o *Outbound) Attach(req *http.Request) error {
	h, err := o.headers()
	if err != nil {
		return err
	}
	return pop.AttachIdentity(req, o.signer, h)
}
