// Command tiercheck prints, for each station an authority knows about, the real
// five-dimension trust vector (from the trust index if one is configured and
// reachable, else "index not running"), the tier that vector earns and whether
// it clears the uplink/downlink rule, AND the operator allow-list decision — kept
// clearly separate, so an operator allow-list is never mistaken for a trust score.
//
//	tiercheck -config deploy/prod/authority.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/arjitsama/overpass/internal/authority"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/planner"
	"github.com/arjitsama/overpass/internal/schema"
	"github.com/arjitsama/overpass/internal/trust"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "tiercheck:", err)
		os.Exit(1)
	}
}

func run(args []string, out *os.File) error {
	fs := flag.NewFlagSet("tiercheck", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "authority config (flight_rules + trust_index) — required")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return fmt.Errorf("-config is required")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	rules := cfg.FlightRules

	// The stations to report: everything named anywhere the authority looks.
	set := map[string]bool{}
	for h := range cfg.TrustIndex.Agents {
		set[h] = true
	}
	for _, h := range rules.Stations {
		set[h] = true
	}
	for _, hosts := range rules.OperatorAllow {
		for _, h := range hosts {
			set[h] = true
		}
	}
	hosts := make([]string, 0, len(set))
	for h := range set {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	if len(hosts) == 0 {
		return fmt.Errorf("no stations found in %s (trust_index.agents / flight_rules.stations / operator_allow)", *cfgPath)
	}

	var tc *trust.Client
	if cfg.TrustIndex.URL != "" {
		tc = &trust.Client{BaseURL: cfg.TrustIndex.URL, Agents: cfg.TrustIndex.Agents}
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATION\tINTEG\tIDENT\tSOLV\tBEHAV\tSAFE\tVECTOR-TIER\tUPLINK\tDOWNLINK")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, h := range hosts {
		integ, ident, solv, behav, safe := "-", "-", "-", "-", "-"
		vecTier := "n/a"
		vectorClearsUplink := false
		vectorClearsDownlink := false
		if tc != nil {
			eval, err := tc.Evaluate(ctx, h)
			if err != nil {
				vecTier = "index-unreachable"
			} else {
				integ = itoa(eval.Integrity)
				ident = itoa(eval.Identity)
				solv = itoa(eval.Solvency)
				behav = itoa(eval.Behavior)
				safe = itoa(eval.Safety)
				vecTier = authority.OverpassTier(eval, rules)
				vectorClearsUplink = vecTier == planner.TierFiduciary
				vectorClearsDownlink = vecTier == planner.TierFiduciary || vecTier == planner.TierTransactional
			}
		} else {
			vecTier = "index-not-configured"
		}
		up := decision(contains(rules.OperatorAllow[schema.ModeUplink], h), vectorClearsUplink)
		down := decision(contains(rules.OperatorAllow[schema.ModeDownlink], h), vectorClearsDownlink)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h, integ, ident, solv, behav, safe, vecTier, up, down)
	}
	_ = w.Flush()
	fmt.Fprintln(out, "\nUPLINK/DOWNLINK: how the mode is granted — 'operator-allow' (flight rules name it),")
	fmt.Fprintln(out, "'vector' (the trust vector clears the rule on its own), or 'REFUSED'.")
	return nil
}

// decision reports how a mode is granted: the operator allow-list wins and is
// labeled as such; otherwise it is granted only if the vector clears the rule.
func decision(operatorAllowed, vectorClears bool) string {
	switch {
	case operatorAllowed:
		return "operator-allow"
	case vectorClears:
		return "vector"
	default:
		return "REFUSED"
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
