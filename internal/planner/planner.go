// Package planner turns a pass table into a booking order. It is greedy,
// deterministic and explainable (master plan 7.6-7.9):
//
//	score (centipoints) = 100*max_elevation_deg + 100*priority_bonus - points_per_dollar*amount_cents
//
// A station is eligible only if it verified and its trust tier allows the
// mode (uplink needs FIDUCIARY, downlink TRANSACTIONAL or better). Eligible
// passes are taken best-first when they do not overlap one already taken
// (one spacecraft, one link at a time) until the contact-time goal is met.
// Every pass not taken stays in the plan with the reason.
package planner

import (
	"errors"
	"fmt"
	"sort"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/passes"
	"github.com/arjitsama/overpass/internal/schema"
)

// Trust tiers (master plan 11).
const (
	TierReadOnly      = "READ_ONLY"
	TierTransactional = "TRANSACTIONAL"
	TierFiduciary     = "FIDUCIARY"
)

// Station is what the planner knows about one ground station.
type Station struct {
	Host          string
	Verified      bool
	VerifyReason  string // why verification failed, shown when !Verified
	Tier          string
	PriorityBonus int64
}

// Request says what the operator wants.
type Request struct {
	Mode            string `json:"mode"`              // schema.ModeUplink or schema.ModeDownlink
	GoalS           int64  `json:"goal_s"`            // stop once this much contact time is booked
	PointsPerDollar int64  `json:"points_per_dollar"` // price weight: score points lost per dollar
}

// Bounds that keep the integer score far from overflow. Quotes are
// station-supplied, so a price outside them is refused, not clamped.
const (
	MaxPriceCents      = 1_000_000_00 // one million dollars per pass
	MaxPointsPerDollar = 10_000
)

// Validate checks a request.
func (r Request) Validate() error {
	if r.Mode != schema.ModeUplink && r.Mode != schema.ModeDownlink {
		return errs.New(errs.BadRequest, fmt.Sprintf("mode %q must be uplink or downlink", r.Mode))
	}
	if r.GoalS <= 0 {
		return errs.New(errs.BadRequest, "goal_s must be positive")
	}
	if r.PointsPerDollar < 0 || r.PointsPerDollar > MaxPointsPerDollar {
		return errs.New(errs.BadRequest, fmt.Sprintf("points_per_dollar must be 0-%d", MaxPointsPerDollar))
	}
	return nil
}

// Candidate is one pass with its price and score.
type Candidate struct {
	Pass        passes.Pass `json:"pass"`
	AmountCents int64       `json:"amount_cents"`
	Score       int64       `json:"score_centipoints"`
	Selected    bool        `json:"selected"`
	Code        errs.Code   `json:"code,omitempty"`   // why it was not selected
	Reason      string      `json:"reason,omitempty"` // the same, in words
}

// Plan is the planner's output. Selected is in AOS order.
type Plan struct {
	Request    Request     `json:"request"`
	Selected   []Candidate `json:"selected"`
	Others     []Candidate `json:"others"`
	ContactS   int64       `json:"contact_s"`
	GoalMet    bool        `json:"goal_met"`
	Excluded   []string    `json:"excluded_hosts,omitempty"` // removed by Replan
	candidates []Candidate
	stations   map[string]Station
	prices     map[string]int64
}

// PriceKey keys a price by station host and AOS.
func PriceKey(p passes.Pass) string { return fmt.Sprintf("%s@%d", p.Host, p.AOS) }

// Schedule plans req over table. prices maps PriceKey to integer cents; a
// pass with no price is not bookable. Station hosts must be unique.
func Schedule(table []passes.Pass, stations []Station, prices map[string]int64, req Request) (Plan, error) {
	if err := req.Validate(); err != nil {
		return Plan{}, err
	}
	st := map[string]Station{}
	for _, s := range stations {
		if s.Host == "" {
			return Plan{}, errs.New(errs.BadRequest, "station with empty host")
		}
		if _, dup := st[s.Host]; dup {
			return Plan{}, errs.New(errs.BadRequest, "duplicate station "+s.Host)
		}
		st[s.Host] = s
	}
	return schedule(table, st, prices, req, nil), nil
}

// ErrNoPlan is returned by Replan on a zero Plan.
var ErrNoPlan = errors.New("planner: no plan to replan")

func schedule(table []passes.Pass, st map[string]Station, prices map[string]int64, req Request, excluded []string) Plan {
	ex := map[string]bool{}
	for _, h := range excluded {
		ex[h] = true
	}
	plan := Plan{Request: req, Excluded: excluded, stations: st, prices: prices}
	var eligible []Candidate
	for _, p := range table {
		c := Candidate{Pass: p}
		price, priced := prices[PriceKey(p)]
		c.AmountCents = price
		s, known := st[p.Host]
		switch {
		case ex[p.Host]:
			c.Code, c.Reason = errs.PlanExcluded, "station removed by replan"
		case !known:
			c.Code, c.Reason = errs.PlanUnknownHost, "unknown station"
		case !s.Verified:
			c.Code, c.Reason = errs.PlanUnverified, "not verified: "+s.VerifyReason
		case !tierAllows(s.Tier, req.Mode):
			c.Code, c.Reason = errs.PolicyRefusedTier, fmt.Sprintf("tier %s does not allow %s", tierName(s.Tier), req.Mode)
		case !priced:
			c.Code, c.Reason = errs.PlanNoQuote, "no quote"
		case price < 0 || price > MaxPriceCents:
			c.Code, c.Reason = errs.PlanBadPrice, fmt.Sprintf("quoted price %d cents is outside 0-%d", price, int64(MaxPriceCents))
		default:
			c.Score = 100*p.MaxElevationDeg + 100*s.PriorityBonus - req.PointsPerDollar*price
			eligible = append(eligible, c)
			continue
		}
		plan.Others = append(plan.Others, c)
	}
	sort.SliceStable(eligible, func(i, j int) bool { return better(eligible[i], eligible[j]) })
	for _, c := range eligible {
		switch {
		case plan.GoalMet:
			c.Code, c.Reason = errs.PlanGoalMet, "contact goal already met"
		case overlapsAny(c.Pass, plan.Selected):
			c.Code, c.Reason = errs.PlanOverlap, "overlaps a better pass already selected"
		default:
			c.Selected = true
			plan.Selected = append(plan.Selected, c)
			plan.ContactS += c.Pass.DurationS
			plan.GoalMet = plan.ContactS >= req.GoalS
			continue
		}
		plan.Others = append(plan.Others, c)
	}
	sort.SliceStable(plan.Selected, func(i, j int) bool { return byTime(plan.Selected[i], plan.Selected[j]) })
	sort.SliceStable(plan.Others, func(i, j int) bool { return byTime(plan.Others[i], plan.Others[j]) })
	plan.candidates = append(append([]Candidate(nil), plan.Selected...), plan.Others...)
	return plan
}

// better orders by score, then earlier AOS, then host: a total order, so the
// plan does not depend on input order.
func better(a, b Candidate) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return byTime(a, b)
}

func byTime(a, b Candidate) bool {
	if a.Pass.AOS != b.Pass.AOS {
		return a.Pass.AOS < b.Pass.AOS
	}
	return a.Pass.Host < b.Pass.Host
}

func overlapsAny(p passes.Pass, sel []Candidate) bool {
	for _, c := range sel {
		if p.Overlaps(c.Pass) {
			return true
		}
	}
	return false
}

func tierAllows(tier, mode string) bool {
	switch mode {
	case schema.ModeUplink:
		return tier == TierFiduciary
	case schema.ModeDownlink:
		return tier == TierFiduciary || tier == TierTransactional
	}
	return false
}

func tierName(t string) string {
	if t == "" {
		return "none"
	}
	return t
}

// Diff is what a replan changed.
type Diff struct {
	RemovedHost string        `json:"removed_host"`
	Dropped     []passes.Pass `json:"dropped"`
	Added       []passes.Pass `json:"added"`
	Kept        []passes.Pass `json:"kept"`
	BeforeS     int64         `json:"before_contact_s"`
	AfterS      int64         `json:"after_contact_s"`
}

// Replan removes host (after a rejected booking, a session cut or a tier
// drop, master plan 7.9), re-runs the schedule and emits a `replan` event
// with the before and after. emit may be nil.
func Replan(prev Plan, host string, emit func(bus.Event)) (Plan, Diff, error) {
	if prev.stations == nil {
		return Plan{}, Diff{}, ErrNoPlan
	}
	var table []passes.Pass
	for _, c := range prev.candidates {
		table = append(table, c.Pass)
	}
	sort.SliceStable(table, func(i, j int) bool {
		return byTime(Candidate{Pass: table[i]}, Candidate{Pass: table[j]})
	})
	excluded := append(append([]string(nil), prev.Excluded...), host)
	next := schedule(table, prev.stations, prev.prices, prev.Request, excluded)
	d := diff(prev, next, host)
	if emit != nil {
		emit(bus.Event{Kind: "replan", Subject: host, Result: "ok",
			Reason: fmt.Sprintf("removed %s: %d dropped, %d added, contact %ds -> %ds",
				host, len(d.Dropped), len(d.Added), d.BeforeS, d.AfterS),
			Data: map[string]any{"before": passList(prev.Selected), "after": passList(next.Selected),
				"dropped": d.Dropped, "added": d.Added}})
	}
	return next, d, nil
}

func diff(before, after Plan, host string) Diff {
	d := Diff{RemovedHost: host, BeforeS: before.ContactS, AfterS: after.ContactS}
	in := func(p passes.Pass, cs []Candidate) bool {
		for _, c := range cs {
			if c.Pass.Host == p.Host && c.Pass.AOS == p.AOS {
				return true
			}
		}
		return false
	}
	for _, c := range before.Selected {
		if in(c.Pass, after.Selected) {
			d.Kept = append(d.Kept, c.Pass)
		} else {
			d.Dropped = append(d.Dropped, c.Pass)
		}
	}
	for _, c := range after.Selected {
		if !in(c.Pass, before.Selected) {
			d.Added = append(d.Added, c.Pass)
		}
	}
	return d
}

func passList(cs []Candidate) []passes.Pass {
	out := make([]passes.Pass, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Pass)
	}
	return out
}
