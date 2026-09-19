package a2a

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

// maxReplyBytes caps a JSON-RPC reply.
const maxReplyBytes = 1 << 20

// Client calls one skill on another agent over A2A 1.0 JSON-RPC.
type Client struct {
	URL  string       // the agent URL (POST /)
	HTTP *http.Client // TLS policy is the caller's
	// Sign, when set, authenticates the request (verify.Outbound.Attach).
	Sign func(*http.Request) error
}

// Call sends {"skill": skill, ...args} as the data part of a SendMessage and
// decodes the reply's data part into out. A JSON-RPC rejection carrying a
// google.rpc.ErrorInfo reason comes back as an *errs.Error with that code; a
// 4xx from an HTTP guard comes back with its {code, detail}.
func (c *Client) Call(ctx context.Context, skill string, args map[string]any, out any) error {
	data := map[string]any{"skill": skill}
	for k, v := range args {
		data[k] = v
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": hex.EncodeToString(id), "method": MethodSendV1,
		"params": map[string]any{"message": map[string]any{"messageId": hex.EncodeToString(id), "role": "ROLE_USER",
			"parts": []any{map[string]any{"data": data, "mediaType": "application/json"}}}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Sign != nil {
		if err := c.Sign(req); err != nil {
			return err
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errs.New(errs.Unavailable, fmt.Sprintf("%s: %v", c.URL, err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return errs.New(errs.Unavailable, err.Error())
	}
	if resp.StatusCode != http.StatusOK {
		var e errs.Error
		if json.Unmarshal(raw, &e) == nil && errs.Known(e.Code) {
			return &e
		}
		return errs.New(errs.Unavailable, fmt.Sprintf("%s: HTTP %d", c.URL, resp.StatusCode))
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
			Message string `json:"message"`
			Data    []struct {
				Reason   string            `json:"reason"`
				Metadata map[string]string `json:"metadata"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return errs.New(errs.Unavailable, "reply is not JSON-RPC")
	}
	if r.Error != nil {
		if len(r.Error.Data) > 0 && errs.Known(errs.Code(r.Error.Data[0].Reason)) {
			return errs.New(errs.Code(r.Error.Data[0].Reason), r.Error.Data[0].Metadata["detail"])
		}
		return errs.New(errs.Unavailable, r.Error.Message)
	}
	if len(r.Result.Message.Parts) == 0 || len(r.Result.Message.Parts[0].Data) == 0 {
		return errs.New(errs.Unavailable, "reply has no data part")
	}
	return json.Unmarshal(r.Result.Message.Parts[0].Data, out)
}
