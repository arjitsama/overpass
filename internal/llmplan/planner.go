package llmplan

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/errs"
	"github.com/arjitsama/overpass/internal/passes"
	"github.com/arjitsama/overpass/internal/planner"
	"github.com/arjitsama/overpass/internal/schema"
)

// Planner is the LLM planner. It implements planner.Planner and always has the
// greedy planner as its fallback and validator.
type Planner struct {
	Client    Model            // the model; nil means "always fall back"
	Proposer  planner.Proposer // forwards propose_booking to the authority
	Fallback  planner.Planner  // usually planner.Greedy{}
	ModelName string           // defaults to DefaultModel
	Timeout   time.Duration    // defaults to 10s
	MaxTurns  int              // defaults to 8
	Emit      func(bus.Event)
}

func (p *Planner) emit(e bus.Event) {
	if p.Emit != nil {
		p.Emit(e)
	}
}

func (p *Planner) fallback() planner.Planner {
	if p.Fallback != nil {
		return p.Fallback
	}
	return planner.Greedy{}
}

// Plan runs the model within a 10-second budget and falls back to the greedy
// plan on any error or timeout, emitting an event either way. No pass is missed
// because a model was slow (master plan §7 rev 3.6).
func (p *Planner) Plan(ctx context.Context, in planner.Input) (planner.Plan, error) {
	if err := in.Request.Validate(); err != nil {
		return planner.Plan{}, err
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	plan, err := p.run(cctx, in)
	if err != nil {
		p.emit(bus.Event{Kind: "llm_plan", Result: "fallback", Reason: err.Error()})
		fb, ferr := p.fallback().Plan(ctx, in) // original ctx: the fallback must still run
		if ferr != nil {
			return planner.Plan{}, ferr
		}
		fb.Source = "greedy"
		fb.Explanation = "" // model unavailable: the dashboard shows the structured plan
		return fb, nil
	}
	plan.Source = "llm"
	p.emit(bus.Event{Kind: "llm_plan", Result: "ok", Reason: plan.Explanation})
	return plan, nil
}

// session holds the lookups and the running result for one Plan call.
type session struct {
	in            planner.Input
	proposer      planner.Proposer
	passByKey     map[string]passes.Pass
	quoteByKey    map[string]schema.Quote
	stationByHost map[string]planner.Station
	accepted      map[string]acceptedRec // key -> accepted proposal
	refused       map[string]planner.Outcome
}

type acceptedRec struct {
	pass   passes.Pass
	amount int64
	id     string
}

func key(host string, aos int64) string { return fmt.Sprintf("%s@%d", host, aos) }

func (p *Planner) run(ctx context.Context, in planner.Input) (planner.Plan, error) {
	if p.Client == nil {
		return planner.Plan{}, fmt.Errorf("no model configured")
	}
	s := &session{
		in: in, proposer: p.Proposer,
		passByKey: map[string]passes.Pass{}, quoteByKey: map[string]schema.Quote{},
		stationByHost: map[string]planner.Station{},
		accepted:      map[string]acceptedRec{}, refused: map[string]planner.Outcome{},
	}
	for _, ps := range in.Table {
		s.passByKey[planner.PriceKey(ps)] = ps
	}
	for k, q := range in.Quotes {
		s.quoteByKey[k] = q
	}
	for _, st := range in.Stations {
		s.stationByHost[st.Host] = st
	}

	messages := []Message{userMessage(textBlock(s.userPrompt()))}
	maxTurns := p.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 8
	}
	explanation := ""
	for turn := 0; turn < maxTurns; turn++ {
		resp, err := p.Client.Create(ctx, MessageRequest{
			Model:      p.modelName(),
			MaxTokens:  1024,
			System:     systemPrompt,
			Tools:      toolDefs,
			ToolChoice: &ToolChoice{Type: "auto", DisableParallelToolUse: true},
			Messages:   messages,
		})
		if err != nil {
			return planner.Plan{}, err
		}
		explanation = lastText(resp.Content, explanation)
		if resp.StopReason != "tool_use" {
			break
		}
		messages = append(messages, assistantMessage(resp.Content))
		var results []Block
		for _, b := range resp.Content {
			if b.Type != "tool_use" {
				continue
			}
			results = append(results, toolResult(b.ID, s.dispatch(ctx, b.Name, b.Input)))
		}
		if len(results) == 0 {
			break
		}
		messages = append(messages, userMessage(results...))
	}
	return s.buildPlan(capStr(strings.TrimSpace(explanation), 2000)), nil
}

func (p *Planner) modelName() string {
	if p.ModelName != "" {
		return p.ModelName
	}
	return DefaultModel
}

func lastText(content []Block, prev string) string {
	for _, b := range content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return b.Text
		}
	}
	return prev
}

// dispatch runs one tool call and returns a JSON string result. Every payload is
// built from structured fields only; station free text (in.Notes) is never read.
func (s *session) dispatch(ctx context.Context, name string, input json.RawMessage) string {
	switch name {
	case "list_passes":
		views := make([]passView, 0, len(s.in.Table))
		for _, ps := range s.in.Table {
			views = append(views, viewPass(ps, s.in.Request.Mode))
		}
		return mustJSON(map[string]any{"mode": s.in.Request.Mode, "passes": views})
	case "get_trust":
		var args struct {
			Host string `json:"host"`
		}
		_ = json.Unmarshal(input, &args)
		st, ok := s.stationByHost[args.Host]
		if !ok {
			return mustJSON(map[string]any{"error": "unknown station"})
		}
		return mustJSON(viewTrust(st))
	case "get_pass_quote":
		var args struct {
			Host string `json:"host"`
			AOS  int64  `json:"aos"`
		}
		_ = json.Unmarshal(input, &args)
		q, ok := s.quoteByKey[key(args.Host, args.AOS)]
		if !ok {
			return mustJSON(map[string]any{"error": "no quote for that pass"})
		}
		return mustJSON(viewQuote(args.Host, q))
	case "propose_booking":
		return s.propose(ctx, input)
	default:
		return mustJSON(map[string]any{"error": "unknown tool"})
	}
}

func (s *session) propose(ctx context.Context, input json.RawMessage) string {
	var args struct {
		Host string `json:"host"`
		AOS  int64  `json:"aos"`
	}
	_ = json.Unmarshal(input, &args)
	k := key(args.Host, args.AOS)
	q, ok := s.quoteByKey[k]
	if !ok {
		return mustJSON(map[string]any{"accepted": false, "reason": "no quote for that pass"})
	}
	if s.proposer == nil {
		return mustJSON(map[string]any{"accepted": false, "reason": "no authority configured"})
	}
	out, err := s.proposer.Propose(ctx, planner.Proposal{Quote: q, Mode: s.in.Request.Mode})
	if err != nil {
		s.refused[k] = planner.Outcome{Reason: capStr(err.Error(), 256)}
		return mustJSON(map[string]any{"accepted": false, "reason": capStr(err.Error(), 256)})
	}
	if out.Accepted {
		s.accepted[k] = acceptedRec{pass: s.passByKey[k], amount: q.AmountCents, id: out.MandateID}
		return mustJSON(map[string]any{"accepted": true, "mandate_id": out.MandateID})
	}
	s.refused[k] = out
	return mustJSON(map[string]any{"accepted": false, "code": out.Code, "reason": capStr(out.Reason, 256)})
}

// buildPlan turns the accepted proposals into a planner.Plan, keeping every
// other pass in Others with a reason (a refusal code, or "not proposed").
func (s *session) buildPlan(explanation string) planner.Plan {
	plan := planner.Plan{Request: s.in.Request, Explanation: explanation}
	seen := map[string]bool{}
	for k, rec := range s.accepted {
		seen[k] = true
		plan.Selected = append(plan.Selected, planner.Candidate{
			Pass: rec.pass, AmountCents: rec.amount, Selected: true})
		plan.ContactS += rec.pass.DurationS
	}
	for _, ps := range s.in.Table {
		k := planner.PriceKey(ps)
		if seen[k] {
			continue
		}
		c := planner.Candidate{Pass: ps}
		if q, ok := s.quoteByKey[k]; ok {
			c.AmountCents = q.AmountCents
		}
		if out, ok := s.refused[k]; ok {
			c.Code, c.Reason = errs.Code(out.Code), out.Reason
		} else {
			c.Reason = "not proposed by the planner"
		}
		plan.Others = append(plan.Others, c)
	}
	plan.GoalMet = plan.ContactS >= s.in.Request.GoalS
	sort.SliceStable(plan.Selected, func(i, j int) bool { return byAOS(plan.Selected[i], plan.Selected[j]) })
	sort.SliceStable(plan.Others, func(i, j int) bool { return byAOS(plan.Others[i], plan.Others[j]) })
	return plan
}

func byAOS(a, b planner.Candidate) bool {
	if a.Pass.AOS != b.Pass.AOS {
		return a.Pass.AOS < b.Pass.AOS
	}
	return a.Pass.Host < b.Pass.Host
}

func (s *session) userPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan %s passes for NORAD satellite. Goal: book at least %d seconds of contact time.\n",
		s.in.Request.Mode, s.in.Request.GoalS)
	if ctx := strings.TrimSpace(s.in.Context); ctx != "" {
		fmt.Fprintf(&b, "Operator mission context: %s\n", capStr(ctx, 1000))
	}
	b.WriteString("Use the tools to inspect passes, trust and quotes, then call propose_booking for each pass you want. ")
	b.WriteString("The authority may refuse a proposal on policy; that is expected. Finish with two plain sentences explaining your choices.")
	return b.String()
}
