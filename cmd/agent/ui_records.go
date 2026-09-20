package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/arjitsama/overpass/internal/errs"
)

// maxRecordBytes caps the operator-supplied record files these routes serve.
const maxRecordBytes = 256 << 10

// uiBatteryLive serves the record of the last LIVE battery run against
// production (bin/battery -record). There is no fallback to recorded data: if
// no live run has been recorded, the dashboard says so.
func (a *agent) uiBatteryLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		errs.Write(w, http.StatusMethodNotAllowed, errs.MethodNotAllowed, "use GET")
		return
	}
	raw, err := readRecord(a.cfg.UI.BatteryLiveFile)
	if err != nil {
		errs.Write(w, http.StatusNotFound, errs.NotFound, "no live battery run recorded")
		return
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		errs.Write(w, http.StatusInternalServerError, errs.Internal, "battery record is not valid JSON")
		return
	}
	writeJSON(w, v)
}

// fraudRow is one row of GoDaddy's fraud battery as docs/status/fraud-redteam.md
// records it.
type fraudRow struct {
	N       string `json:"n"`
	Check   string `json:"check"`
	Verdict string `json:"verdict"`
}

// uiFraudRedteam serves the GoDaddy fraud-agent results from
// docs/status/fraud-redteam.md and nothing else — that file is the only source
// of truth for what their battery has returned against us.
func (a *agent) uiFraudRedteam(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		errs.Write(w, http.StatusMethodNotAllowed, errs.MethodNotAllowed, "use GET")
		return
	}
	raw, err := readRecord(a.cfg.UI.FraudRedteamFile)
	if err != nil {
		errs.Write(w, http.StatusNotFound, errs.NotFound, "no fraud red-team record")
		return
	}
	status, rows := parseFraudMarkdown(string(raw))
	writeJSON(w, map[string]any{"status_line": status, "rows": rows})
}

func readRecord(path string) ([]byte, error) {
	if path == "" {
		return nil, os.ErrNotExist
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw := make([]byte, maxRecordBytes)
	n, err := f.Read(raw)
	if n == 0 && err != nil {
		return nil, err
	}
	return raw[:n], nil
}

// parseFraudMarkdown pulls the status sentence and the results table out of the
// red-team log. The status line is reproduced verbatim: the wording rule in
// that file is that no pass is claimed anywhere.
func parseFraudMarkdown(md string) (string, []fraudRow) {
	var status string
	var rows []fraudRow
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if status == "" && strings.HasPrefix(t, "Status ") {
			status = strings.Join(strings.Fields(strings.ReplaceAll(t, "**", "")), " ")
			continue
		}
		if !strings.HasPrefix(t, "|") {
			continue
		}
		cells := splitRow(t)
		// Header, separator and the trailing "not yet" summary row are skipped;
		// a data row starts with a number or a range like "7-16".
		if len(cells) < 4 || cells[0] == "" || cells[0] == "#" || strings.HasPrefix(cells[0], "-") {
			continue
		}
		if !(cells[0][0] >= '0' && cells[0][0] <= '9') {
			continue
		}
		rows = append(rows, fraudRow{N: cells[0], Check: cells[1], Verdict: cells[3]})
	}
	return status, rows
}

func splitRow(line string) []string {
	parts := strings.Split(strings.Trim(line, "|"), "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}
