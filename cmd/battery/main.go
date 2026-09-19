// Command battery runs Overpass's attack checks against a target station and
// prints a verdict per attack (master plan section 10). It exits non-zero if
// any check against a station that should be honest is not BLOCKED, so it can
// gate a deploy.
//
//	battery run     -config battery.yaml            # every attack, table
//	battery run     -config battery.yaml -json
//	battery <name>  -config battery.yaml            # one named attack
//	battery canary  -config battery.yaml            # the two auditor probes
//	battery run     -config battery.yaml -expect-vulnerable   # a rogue target: do not gate
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/arjitsama/overpass/internal/battery"
)

func main() {
	code, err := run(context.Background(), os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "battery:", err)
		os.Exit(2)
	}
	os.Exit(code)
}

// run parses args, runs the requested attacks and returns the process exit
// code (0 = all checks as expected).
func run(ctx context.Context, args []string, out io.Writer) (int, error) {
	if len(args) == 0 {
		return 2, fmt.Errorf("usage: battery run|canary|<attack> -config <file> [-json] [-expect-vulnerable]")
	}
	cmd := args[0]
	fs := flag.NewFlagSet("battery", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "battery config file (required)")
	asJSON := fs.Bool("json", false, "JSON output")
	expectVuln := fs.Bool("expect-vulnerable", false, "the target is expected to be vulnerable (rogue); do not gate")
	if err := fs.Parse(args[1:]); err != nil {
		return 2, err
	}
	if *cfgPath == "" {
		return 2, fmt.Errorf("-config is required")
	}
	b, closeFn, err := load(ctx, *cfgPath)
	if err != nil {
		return 2, err
	}
	defer closeFn()

	var results []battery.Result
	switch cmd {
	case "run":
		results = b.Run(ctx)
	case "canary":
		results = b.Canary(ctx)
	default:
		r, ok := b.Attack(ctx, cmd)
		if !ok {
			return 2, fmt.Errorf("unknown attack %q; try run, canary, or one of %v", cmd, battery.Names())
		}
		results = []battery.Result{r}
	}

	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return 2, err
		}
	} else {
		render(out, results)
	}
	return gate(results, *expectVuln), nil
}

// gate returns the process exit code: an honest target (expectVuln false) must
// have every check BLOCKED; a rogue target must have at least one VULNERABLE.
func gate(results []battery.Result, expectVuln bool) int {
	if expectVuln {
		for _, r := range results {
			if r.Verdict == battery.Vulnerable {
				return 0
			}
		}
		return 1
	}
	if battery.AllBlocked(results) {
		return 0
	}
	return 1
}

// render prints the results as an aligned table.
func render(out io.Writer, results []battery.Result) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ATTACK\tVERDICT\tOBSERVED\tDETAIL")
	blocked, vuln, inconc := 0, 0, 0
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, r.Verdict, r.Observed, r.Detail)
		switch r.Verdict {
		case battery.Blocked:
			blocked++
		case battery.Vulnerable:
			vuln++
		default:
			inconc++
		}
	}
	fmt.Fprintf(w, "\t\t\t%d BLOCKED, %d VULNERABLE, %d INCONCLUSIVE\n", blocked, vuln, inconc)
	_ = w.Flush()
}
