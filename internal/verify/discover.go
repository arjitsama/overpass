package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/arjitsama/overpass/internal/errs"
)

// maxFinderBytes caps a Finder response.
const maxFinderBytes = 1 << 20

// FindByTag searches the environment's ANS Finder for agents tagged tag
// (POST {finder_url}/search, ans spec api-spec-finder-v1.yaml), then runs
// VerifyPeer on each distinct publisher host. Catalog entries are publisher
// content and untrusted; only the verified host is kept from them.
func (v *Verifier) FindByTag(ctx context.Context, envName, tag string) ([]Result, error) {
	e, ok := v.envs[envName]
	if !ok {
		return nil, errs.New(errs.BadRequest, "unknown environment "+envName)
	}
	if e.cfg.FinderURL == "" {
		return nil, errs.New(errs.Unavailable, "environment "+envName+" has no finder_url; tag discovery unavailable")
	}
	hosts, err := e.searchTag(ctx, tag)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(hosts))
	for _, h := range hosts {
		// Finder content is untrusted: match configured peers by host only,
		// and verify anyone else in the environment the search ran in.
		out = append(out, v.verify(ctx, v.peerFor(h, envName, false)))
	}
	return out, nil
}

type finderRequest struct {
	Query    finderQuery `json:"query"`
	PageSize int         `json:"pageSize"`
}

type finderQuery struct {
	Text   string              `json:"text"`
	Filter map[string][]string `json:"filter"`
}

func (e *env) searchTag(ctx context.Context, tag string) ([]string, error) {
	body, _ := json.Marshal(finderRequest{Query: finderQuery{Text: tag, Filter: map[string][]string{"tags": {tag}}}, PageSize: 100})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(e.cfg.FinderURL, "/")+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, errs.New(errs.Unavailable, "finder: "+err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFinderBytes))
	if err != nil {
		return nil, errs.New(errs.Unavailable, "finder: "+err.Error())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errs.New(errs.Unavailable, fmt.Sprintf("finder: %s", resp.Status))
	}
	var out struct {
		Results []struct {
			Identifier string `json:"identifier"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errs.New(errs.Unavailable, "finder: response is not JSON")
	}
	seen := map[string]bool{}
	var hosts []string
	for _, r := range out.Results {
		h := publisherHost(r.Identifier)
		if h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts, nil
}

// publisherHost extracts <publisher> from urn:air:<publisher>:<ns>:<name>;
// the Finder derives that segment from the agent's verified ANS host.
func publisherHost(urn string) string {
	parts := strings.Split(urn, ":")
	if len(parts) < 4 || parts[0] != "urn" || parts[1] != "air" || !validHost(parts[2]) {
		return ""
	}
	return strings.ToLower(parts[2])
}
