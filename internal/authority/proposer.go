package authority

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"

	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/planner"
	"github.com/arjitsama/overpass/internal/schema"
)

// Propose implements planner.Proposer: it forwards the LLM planner's booking
// proposal to issue_mandate, so the flight rules and the trust tier decide, not
// the model (master plan §7 rev 3.3). A named refusal (e.g. POLICY_REFUSED:tier)
// is returned as a non-accepted Outcome, not an error; only an unexpected
// failure is an error.
func (a *Authority) Propose(ctx context.Context, p planner.Proposal) (planner.Outcome, error) {
	q := p.Quote
	if p.Mode != "" {
		q.Mode = p.Mode
	}
	quoteJSON, err := json.Marshal(q)
	if err != nil {
		return planner.Outcome{}, err
	}
	raw, err := json.Marshal(issueArgs{Skill: "issue_mandate", Quote: quoteJSON})
	if err != nil {
		return planner.Outcome{}, err
	}
	out, err := a.IssueMandate(ctx, raw)
	if err != nil {
		var e *errs.Error
		if errors.As(err, &e) {
			return planner.Outcome{Accepted: false, Code: string(e.Code), Reason: e.Detail}, nil
		}
		return planner.Outcome{}, err
	}
	tok := out.(IssueResult).Mandate
	id := ""
	if m, verr := schema.VerifyMandate(tok, []*ecdsa.PublicKey{&a.Key.PublicKey}); verr == nil {
		id = m.MandateID
	}
	return planner.Outcome{Accepted: true, MandateID: id}, nil
}
