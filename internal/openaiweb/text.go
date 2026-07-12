package openaiweb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"imagepool/internal/accounts"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ChatRequest struct {
	Model    string        `json:"model"`
	Prompt   string        `json:"prompt,omitempty"`
	Messages []ChatMessage `json:"messages,omitempty"`
	Stream   bool          `json:"stream,omitempty"`
}

type ChatDelta struct {
	Delta          string `json:"delta"`
	ConversationID string `json:"conversation_id,omitempty"`
}

type ChatResult struct {
	Text           string `json:"text"`
	Model          string `json:"model"`
	ConversationID string `json:"conversation_id,omitempty"`
	AccountEmail   string `json:"account_email,omitempty"`
}

func (c *Client) GenerateText(ctx context.Context, account accounts.Account, req ChatRequest) (ChatResult, error) {
	var text strings.Builder
	conversationID, err := c.StreamText(ctx, account, req, func(delta ChatDelta) error {
		text.WriteString(delta.Delta)
		return nil
	})
	if err != nil {
		return ChatResult{}, err
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "auto"
	}
	return ChatResult{Text: text.String(), Model: model, ConversationID: conversationID, AccountEmail: account.Email}, nil
}

func (c *Client) StreamText(ctx context.Context, account accounts.Account, req ChatRequest, emit func(ChatDelta) error) (string, error) {
	if emit == nil {
		emit = func(ChatDelta) error { return nil }
	}
	if len(req.Messages) == 0 {
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			return "", fmt.Errorf("messages or prompt is required")
		}
		req.Messages = []ChatMessage{{Role: "user", Content: prompt}}
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = "auto"
	}
	scripts, dataBuild, err := c.bootstrapWithResources(ctx, account)
	if err != nil {
		return "", err
	}
	requirements, err := c.chatRequirements(ctx, account, scripts, dataBuild)
	if err != nil {
		return "", err
	}
	path := "/backend-api/conversation"
	timezone := "Asia/Shanghai"
	if strings.TrimSpace(account.AccessToken) == "" {
		path = "/backend-anon/conversation"
		timezone = "America/Los_Angeles"
	}
	payload := c.conversationPayload(req.Messages, req.Model, timezone)
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	for k, v := range c.headers(account, path, path, c.conversationHeaders(requirements)) {
		request.Header.Set(k, v)
	}
	resp, err := c.clientFor(account, false).Do(request)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := ensureOK(resp, path); err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	conversationID := ""
	rawText := ""
	visibleText := ""
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		if conversationID == "" {
			conversationID = ExtractConversationID(payload)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if conversationID == "" {
			conversationID = findStringKey(event, "conversation_id")
		}
		nextRaw := assistantRawText(event, rawText)
		nextVisible := sanitizeText(nextRaw)
		rawText = nextRaw
		if nextVisible != visibleText {
			delta := nextVisible
			if strings.HasPrefix(nextVisible, visibleText) {
				delta = nextVisible[len(visibleText):]
			}
			visibleText = nextVisible
			if delta != "" {
				if err := emit(ChatDelta{Delta: delta, ConversationID: conversationID}); err != nil {
					return conversationID, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return conversationID, err
	}
	return conversationID, nil
}

func (c *Client) conversationPayload(messages []ChatMessage, model, timezone string) map[string]any {
	return map[string]any{
		"action":                        "next",
		"messages":                      c.apiMessagesToConversation(messages),
		"model":                         model,
		"parent_message_id":             c.newID(),
		"conversation_mode":             map[string]any{"kind": "primary_assistant"},
		"conversation_origin":           nil,
		"force_paragen":                 false,
		"force_paragen_model_slug":      "",
		"force_rate_limit":              false,
		"force_use_sse":                 true,
		"history_and_training_disabled": true,
		"reset_rate_limits":             false,
		"suggestions":                   []any{},
		"supported_encodings":           []any{},
		"system_hints":                  []any{},
		"timezone":                      timezone,
		"timezone_offset_min":           -480,
		"variant_purpose":               "comparison_implicit",
		"websocket_request_id":          c.newID(),
		"client_contextual_info": map[string]any{
			"is_dark_mode": false, "time_since_loaded": 120,
			"page_height": 900, "page_width": 1400, "pixel_ratio": 2,
			"screen_height": 1440, "screen_width": 2560,
		},
	}
}

func (c *Client) apiMessagesToConversation(messages []ChatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		text := messageContentText(msg.Content)
		out = append(out, map[string]any{
			"id":      c.newID(),
			"author":  map[string]any{"role": role},
			"content": map[string]any{"content_type": "text", "parts": []string{text}},
		})
	}
	return out
}

func messageContentText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				typ := strings.TrimSpace(str(m["type"]))
				switch typ {
				case "text", "input_text", "output_text":
					b.WriteString(str(m["text"]))
				default:
					if text := str(m["text"]); text != "" {
						b.WriteString(text)
					}
				}
				continue
			}
			if s, ok := item.(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	case []map[string]any:
		var b strings.Builder
		for _, item := range v {
			b.WriteString(str(item["text"]))
		}
		return b.String()
	default:
		return fmt.Sprint(v)
	}
}

func (c *Client) conversationHeaders(req chatRequirements) map[string]string {
	h := map[string]string{"Accept": "text/event-stream", "Content-Type": "application/json", "OpenAI-Sentinel-Chat-Requirements-Token": req.Token}
	if req.ProofToken != "" {
		h["OpenAI-Sentinel-Proof-Token"] = req.ProofToken
	}
	if req.TurnstileToken != "" {
		h["OpenAI-Sentinel-Turnstile-Token"] = req.TurnstileToken
	}
	if req.SOToken != "" {
		h["OpenAI-Sentinel-SO-Token"] = req.SOToken
	}
	return h
}

func assistantRawText(event map[string]any, current string) string {
	for _, candidate := range []any{event, event["v"]} {
		m, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		message, ok := m["message"].(map[string]any)
		if !ok {
			continue
		}
		author, _ := message["author"].(map[string]any)
		if strings.ToLower(str(author["role"])) != "assistant" {
			continue
		}
		if text := assistantMessageText(message); text != "" {
			return text
		}
	}
	return applyTextPatch(event, current)
}

func assistantMessageText(message map[string]any) string {
	content, _ := message["content"].(map[string]any)
	if parts, ok := content["parts"].([]any); ok {
		var b strings.Builder
		for _, part := range parts {
			if s, ok := part.(string); ok {
				b.WriteString(s)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return str(content["text"])
}

func applyTextPatch(event map[string]any, current string) string {
	if str(event["p"]) == "/message/content/parts/0" {
		return applyPatchOp(event, current)
	}
	if str(event["o"]) == "patch" {
		if ops, ok := event["v"].([]any); ok {
			text := current
			for _, op := range ops {
				if m, ok := op.(map[string]any); ok {
					text = applyTextPatch(m, text)
				}
			}
			return text
		}
	}
	if s, ok := event["v"].(string); ok && current != "" && event["p"] == nil && event["o"] == nil {
		return current + s
	}
	if ops, ok := event["v"].([]any); ok {
		text := current
		for _, op := range ops {
			if m, ok := op.(map[string]any); ok {
				text = applyTextPatch(m, text)
			}
		}
		return text
	}
	return current
}

func applyPatchOp(op map[string]any, current string) string {
	switch str(op["o"]) {
	case "append":
		return current + str(op["v"])
	case "replace":
		return str(op["v"])
	default:
		return current
	}
}

func sanitizeText(text string) string {
	text = strings.ReplaceAll(text, "\ue200", "")
	text = strings.ReplaceAll(text, "\ue201", "")
	text = strings.ReplaceAll(text, "\ue202", " ")
	return text
}

var _ = time.Second
