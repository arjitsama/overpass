package llmplan

import (
	"encoding/json"

	"github.com/arjitsama/overpass/internal/passes"
	"github.com/arjitsama/overpass/internal/planner"
	"github.com/arjitsama/overpass/internal/schema"
)

// maxFieldLen caps every string the model sees. Station-controlled strings
// (hosts) are attacker-influenced, so they are truncated, not trusted.
const maxFieldLen = 128

// capStr truncates s to at most n runes.
func capStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// passView is the only shape of a pass the model sees: structured fields, no
// free text.
type passView struct {
	Host       string `json:"host"`
	Mode       string `json:"mode"`
	AOS        int64  `json:"aos"`
	LOS        int64  `json:"los"`
	MaxElevDeg int64  `json:"max_elevation_deg"`
	DurationS  int64  `json:"duration_s"`
}

func viewPass(p passes.Pass, mode string) passView {
	return passView{Host: capStr(p.Host, maxFieldLen), Mode: mode, AOS: p.AOS, LOS: p.LOS,
		MaxElevDeg: p.MaxElevationDeg, DurationS: p.DurationS}
}

// trustView is a station's structured trust facts. It deliberately omits any
// station-supplied free text (card descriptions, quote notes).
type trustView struct {
	Host          string `json:"host"`
	Verified      bool   `json:"verified"`
	Tier          string `json:"tier"`
	PriorityBonus int64  `json:"priority_bonus"`
}

func viewTrust(s planner.Station) trustView {
	return trustView{Host: capStr(s.Host, maxFieldLen), Verified: s.Verified,
		Tier: capStr(s.Tier, maxFieldLen), PriorityBonus: s.PriorityBonus}
}

// acceptView is one structured payment option.
type acceptView struct {
	Scheme  string `json:"scheme"`
	Network string `json:"network"`
	Asset   string `json:"asset"`
	Amount  int64  `json:"amount"`
	PayTo   string `json:"pay_to"`
}

// quoteView is the structured quote the model sees for a pass. schema.Quote
// carries no free-text note (the wire schema is frozen); even so, this view is
// built field by field so no future free text can leak into a prompt.
type quoteView struct {
	Host        string       `json:"host"`
	AOS         int64        `json:"aos"`
	LOS         int64        `json:"los"`
	Mode        string       `json:"mode"`
	AmountCents int64        `json:"amount_cents"`
	MaxElevDeg  int64        `json:"max_elevation_deg"`
	Accepts     []acceptView `json:"accepts"`
}

func viewQuote(host string, q schema.Quote) quoteView {
	v := quoteView{Host: capStr(host, maxFieldLen), AOS: q.AOS, LOS: q.LOS,
		Mode: capStr(q.Mode, maxFieldLen), AmountCents: q.AmountCents, MaxElevDeg: q.MaxElevationDeg}
	for _, a := range q.Accepts {
		v.Accepts = append(v.Accepts, acceptView{
			Scheme: capStr(a.Scheme, maxFieldLen), Network: capStr(a.Network, maxFieldLen),
			Asset: capStr(a.Asset, maxFieldLen), Amount: a.Amount, PayTo: capStr(a.PayTo, maxFieldLen)})
	}
	return v
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"encoding failed"}`
	}
	return string(b)
}
