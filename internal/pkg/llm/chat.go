package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type Image struct {
	Data   []byte
	MIME   string // image/jpeg, image/png
	Width  int    // for log summaries only
	Height int
}

type Message struct {
	Role   string // user | assistant
	Text   string
	Images []Image
}

type ChatRequest struct {
	Purpose     string
	AssetID     int64
	System      string
	Messages    []Message
	JSON        bool // ask for a JSON object (response_format json_object)
	MaxTokens   int
	Temperature *float64
	// Fallback produces a local answer when every attempt fails. Optional.
	Fallback func() (string, error)
}

type ChatResponse struct {
	Text     string
	Model    string
	Usage    Usage
	Fallback bool
}

type chatWire struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Chat sends a (possibly multimodal) conversation to the VLM.
func (g *Gateway) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body := map[string]any{"model": g.vlm.model, "messages": wireMessages(req)}
	for k, v := range g.vlm.extra {
		body[k] = v
	}
	if req.JSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	var fb func() (*ChatResponse, string, error)
	if req.Fallback != nil {
		fb = func() (*ChatResponse, string, error) {
			text, err := req.Fallback()
			return &ChatResponse{Text: text, Model: "local-rule", Fallback: true}, text, err
		}
	}
	resp, _, err := run(ctx, g, g.vlm, callMeta{purpose: req.Purpose, assetID: req.AssetID, input: summarizeChat(req)},
		func(ctx context.Context) (attemptResult[*ChatResponse], error) {
			var w chatWire
			if err := g.postJSON(ctx, g.vlm, body, &w); err != nil {
				return attemptResult[*ChatResponse]{}, err
			}
			if len(w.Choices) == 0 {
				return attemptResult[*ChatResponse]{}, fmt.Errorf("%w: no choices", errBadResponse)
			}
			text := w.Choices[0].Message.Content
			if req.JSON && extractJSON(text) == "" {
				// The model ignored JSON mode; another attempt usually fixes it.
				return attemptResult[*ChatResponse]{}, fmt.Errorf("%w: no JSON object in reply", errBadResponse)
			}
			u := Usage{w.Usage.PromptTokens, w.Usage.CompletionTokens, w.Usage.TotalTokens}
			return attemptResult[*ChatResponse]{value: &ChatResponse{Text: text, Model: w.Model, Usage: u},
				usage: u, output: text}, nil
		}, fb)
	return resp, err
}

// ChatJSON is Chat with JSON mode, decoding the reply into out.
func (g *Gateway) ChatJSON(ctx context.Context, req ChatRequest, out any) (*ChatResponse, error) {
	req.JSON = true
	resp, err := g.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(extractJSON(resp.Text)), out); err != nil {
		return resp, fmt.Errorf("decode model JSON: %w", err)
	}
	return resp, nil
}

func wireMessages(req ChatRequest) []map[string]any {
	var msgs []map[string]any
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		if len(m.Images) == 0 {
			msgs = append(msgs, map[string]any{"role": role, "content": m.Text})
			continue
		}
		parts := make([]map[string]any, 0, len(m.Images)+1)
		for _, img := range m.Images {
			mime := img.MIME
			if mime == "" {
				mime = "image/jpeg"
			}
			url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}})
		}
		if m.Text != "" {
			parts = append(parts, map[string]any{"type": "text", "text": m.Text})
		}
		msgs = append(msgs, map[string]any{"role": role, "content": parts})
	}
	return msgs
}

// summarizeChat renders a request for the call log: text kept, images
// replaced by a short description, so base64 payloads never hit the log.
func summarizeChat(req ChatRequest) string {
	var b strings.Builder
	if req.System != "" {
		b.WriteString("[system] " + req.System + " ")
	}
	for _, m := range req.Messages {
		for _, img := range m.Images {
			fmt.Fprintf(&b, "[image %dx%d %s %dKB] ", img.Width, img.Height, img.MIME, len(img.Data)/1024)
		}
		b.WriteString(m.Text + " ")
	}
	return strings.TrimSpace(b.String())
}

// extractJSON returns the first top-level JSON object or array in s,
// tolerating ```json fences and prose around it. Empty if none is found.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		if j := strings.Index(rest, "```"); j >= 0 {
			s = strings.TrimSpace(rest[:j])
		}
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return ""
	}
	open, close := s[start], byte('}')
	if open == '[' {
		close = ']'
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == open:
			depth++
		case c == close:
			depth--
			if depth == 0 {
				cand := s[start : i+1]
				if json.Valid([]byte(cand)) {
					return cand
				}
				return ""
			}
		}
	}
	return ""
}
