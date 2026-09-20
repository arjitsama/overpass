package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/arjitsama/overpass/internal/battery"
)

// Target names what a run was aimed at, so a record can never be read as
// evidence about a host it never touched.
type Target struct {
	StationHost   string `json:"station_host"`
	StationANS    string `json:"station_ans"`
	StationURL    string `json:"station_url"`
	SpacecraftURL string `json:"spacecraft_url"`
}

// Record is one live battery run against a named target, with the date it ran.
// The dashboard reads it so the attack-battery card shows a real run and when
// it happened, never a recorded replay presented as live.
type Record struct {
	RanAt        string           `json:"ran_at"`
	Target       Target           `json:"target"`
	Blocked      int              `json:"blocked"`
	Vulnerable   int              `json:"vulnerable"`
	Inconclusive int              `json:"inconclusive"`
	Total        int              `json:"total"`
	Results      []battery.Result `json:"results"`
}

// NewRecord counts the verdicts and stamps the run.
func NewRecord(target Target, results []battery.Result, now time.Time) Record {
	r := Record{RanAt: now.UTC().Format(time.RFC3339), Target: target, Total: len(results), Results: results}
	for _, res := range results {
		switch res.Verdict {
		case battery.Blocked:
			r.Blocked++
		case battery.Vulnerable:
			r.Vulnerable++
		default:
			r.Inconclusive++
		}
	}
	return r
}

// writeRecord writes <base>.json for the dashboard and <base>.md for the repo.
func writeRecord(base string, target Target, results []battery.Result, now time.Time) error {
	rec := NewRecord(target, results, now)
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(base+".json", append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(base+".md", []byte(rec.Markdown()), 0o644)
}

// Markdown renders the record as the status doc committed to the repo.
func (r Record) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Attack battery — last live run\n\n")
	fmt.Fprintf(&b, "Last live run: %s against `%s`.\n\n", r.RanAt, r.Target.StationHost)
	fmt.Fprintf(&b, "%d of %d blocked", r.Blocked, r.Total)
	if r.Inconclusive > 0 {
		fmt.Fprintf(&b, ", %d inconclusive", r.Inconclusive)
	}
	if r.Vulnerable > 0 {
		fmt.Fprintf(&b, ", **%d vulnerable**", r.Vulnerable)
	}
	fmt.Fprintf(&b, ". Station `%s`, spacecraft `%s`.\n\n", r.Target.StationANS, r.Target.SpacecraftURL)
	fmt.Fprintf(&b, "Written by `battery run -record`; every row is one real A2A call the\n")
	fmt.Fprintf(&b, "target refused. An INCONCLUSIVE row says why it could not run — it is\n")
	fmt.Fprintf(&b, "never counted as blocked.\n\n")
	fmt.Fprintf(&b, "| attack | verdict | expected | observed | detail |\n|---|---|---|---|---|\n")
	for _, res := range r.Results {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			res.Name, res.Verdict, res.Expected, res.Observed, mdCell(res.Detail))
	}
	return b.String()
}

// mdCell keeps a detail string inside its table cell.
func mdCell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}
