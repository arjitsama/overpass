// Package battery runs every Overpass attack as a check with a verdict, and
// drives the auditor's targets. Each attack obtains a real quote (and, where
// needed, a real or crafted mandate), attempts book_pass over A2A with a
// chosen DPoP behaviour, and reports the observed rejection code (master plan
// section 10).
package battery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/arjitsama/overpass/internal/errs"
)

const maxReplyBytes = 1 << 20

// signer authenticates an outbound request (verify.Outbound.Attach), or is
// nil for an unauthenticated call.
type signer interface {
	Attach(*http.Request) error
}

// call sends one SendMessage carrying {"skill": skill, ...data} to url,
// signed by sign (nil for no DPoP). It returns the reply's data part, the
// observed errs.Code (empty on success), and a transport error.
func (b *Battery) call(ctx context.Context, url string, sign signer, skill string, data map[string]any) (json.RawMessage, errs.Code, error) {
	req, err := b.request(ctx, url, skill, data)
	if err != nil {
		return nil, "", err
	}
	if sign != nil {
		if err := sign.Attach(req); err != nil {
			return nil, "", err
		}
	}
	return b.do(req)
}

// callReplay signs one request and sends it twice, returning the code of each
// send. The second reuses the exact DPoP proof (attack 1, replay_booking).
func (b *Battery) callReplay(ctx context.Context, url string, sign signer, skill string, data map[string]any) (first, second errs.Code, err error) {
	body, err := b.body(skill, data)
	if err != nil {
		return "", "", err
	}
	req, err := b.newRequest(ctx, url, body)
	if err != nil {
		return "", "", err
	}
	if err := sign.Attach(req); err != nil {
		return "", "", err
	}
	replay, err := b.newRequest(ctx, url, body)
	if err != nil {
		return "", "", err
	}
	replay.Header = req.Header.Clone()
	if _, first, err = b.do(req); err != nil {
		return "", "", err
	}
	_, second, err = b.do(replay)
	return first, second, err
}

func (b *Battery) request(ctx context.Context, url, skill string, data map[string]any) (*http.Request, error) {
	body, err := b.body(skill, data)
	if err != nil {
		return nil, err
	}
	return b.newRequest(ctx, url, body)
}

func (b *Battery) body(skill string, data map[string]any) ([]byte, error) {
	part := map[string]any{"skill": skill}
	for k, v := range data {
		part[k] = v
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": hex.EncodeToString(id), "method": "SendMessage",
		"params": map[string]any{"message": map[string]any{"messageId": hex.EncodeToString(id), "role": "ROLE_USER",
			"parts": []any{map[string]any{"data": part, "mediaType": "application/json"}}}}})
}

func (b *Battery) newRequest(ctx context.Context, url string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// do sends req and returns the reply data part and the observed code.
func (b *Battery) do(req *http.Request) (json.RawMessage, errs.Code, error) {
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		var e errs.Error
		if json.Unmarshal(raw, &e) == nil && errs.Known(e.Code) {
			return nil, e.Code, nil
		}
		return nil, "", fmt.Errorf("%s: HTTP %d: %s", req.URL, resp.StatusCode, truncate(raw))
	}
	var r struct {
		Result struct {
			Message struct {
				Parts []struct {
					Data json.RawMessage `json:"data"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"result"`
		Error *struct {
			Data []struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", fmt.Errorf("reply not JSON-RPC: %s", truncate(raw))
	}
	if r.Error != nil {
		if len(r.Error.Data) > 0 && errs.Known(errs.Code(r.Error.Data[0].Reason)) {
			return nil, errs.Code(r.Error.Data[0].Reason), nil
		}
		return nil, "", fmt.Errorf("unnamed JSON-RPC error: %s", truncate(raw))
	}
	if len(r.Result.Message.Parts) == 0 {
		return nil, "", fmt.Errorf("reply has no data part")
	}
	return r.Result.Message.Parts[0].Data, "", nil
}

// get fetches a served file (agent card, trust card) from the target.
func (b *Battery) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
}

func truncate(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}
