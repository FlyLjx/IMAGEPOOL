package openaiweb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"imagepool/internal/accounts"
)

var searchURLRE = regexp.MustCompile(`https?://[^\s"'<>）)\]}]+`)

func (c *Client) Search(ctx context.Context, account accounts.Account, req SearchRequest) (SearchResult, error) {
	if strings.TrimSpace(account.AccessToken) == "" {
		return SearchResult{}, fmt.Errorf("access_token is required for search")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return SearchResult{}, fmt.Errorf("prompt is required")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "gpt-5-5"
	}
	timeout := seconds(req.TimeoutSecs)
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	interval := seconds(req.PollIntervalSecs)
	if interval <= 0 {
		interval = 3 * time.Second
	}
	conduit, err := c.prepareSearchConversation(ctx, account, prompt, model)
	if err != nil {
		return SearchResult{}, err
	}
	scripts, dataBuild, err := c.bootstrapWithResources(ctx, account)
	if err != nil {
		return SearchResult{}, err
	}
	requirements, err := c.chatRequirements(ctx, account, scripts, dataBuild)
	if err != nil {
		return SearchResult{}, err
	}
	conversationID, err := c.runSearchConversation(ctx, account, prompt, model, conduit, requirements)
	if err != nil {
		return SearchResult{}, err
	}
	result, err := c.waitSearchResult(ctx, account, conversationID, timeout, interval)
	if err != nil {
		return SearchResult{}, err
	}
	result.AccountEmail = account.Email
	result.Model = model
	return result, nil
}

func (c *Client) prepareSearchConversation(ctx context.Context, account accounts.Account, prompt, model string) (string, error) {
	path := "/backend-api/f/conversation/prepare"
	payload := map[string]any{
		"action":                "next",
		"fork_from_shared_post": false,
		"parent_message_id":     "client-created-root",
		"model":                 model,
		"client_prepare_state":  "success",
		"timezone_offset_min":   -480,
		"timezone":              "Asia/Shanghai",
		"conversation_mode":     map[string]any{"kind": "primary_assistant"},
		"system_hints":          []string{"search"},
		"partial_query": map[string]any{
			"id":      c.newID(),
			"author":  map[string]any{"role": "user"},
			"content": map[string]any{"content_type": "text", "parts": []string{prompt}},
		},
		"supports_buffering":     true,
		"supported_encodings":    []string{"v1"},
		"client_contextual_info": map[string]any{"app_name": "chatgpt.com"},
	}
	var out struct {
		ConduitToken string `json:"conduit_token"`
	}
	extra := map[string]string{"Accept": "*/*", "Content-Type": "application/json", "X-Conduit-Token": "no-token"}
	if err := c.doJSON(ctx, account, http.MethodPost, path, path, payload, extra, &out); err != nil {
		return "", err
	}
	if out.ConduitToken == "" {
		return "", fmt.Errorf("missing conduit_token")
	}
	return out.ConduitToken, nil
}

func (c *Client) runSearchConversation(ctx context.Context, account accounts.Account, prompt, model, conduit string, requirements chatRequirements) (string, error) {
	path := "/backend-api/f/conversation"
	payload := map[string]any{
		"action": "next",
		"messages": []any{map[string]any{
			"id":          c.newID(),
			"author":      map[string]any{"role": "user"},
			"create_time": float64(time.Now().UnixNano()) / 1e9,
			"content":     map[string]any{"content_type": "text", "parts": []string{prompt}},
			"metadata": map[string]any{
				"developer_mode_connector_ids": []any{},
				"selected_github_repos":        []any{},
				"selected_all_github_repos":    false,
				"system_hints":                 []string{"search"},
				"serialization_metadata":       map[string]any{"custom_symbol_offsets": []any{}},
			},
		}},
		"parent_message_id":                    "client-created-root",
		"model":                                model,
		"client_prepare_state":                 "success",
		"timezone_offset_min":                  -480,
		"timezone":                             "Asia/Shanghai",
		"conversation_mode":                    map[string]any{"kind": "primary_assistant"},
		"enable_message_followups":             true,
		"system_hints":                         []string{},
		"supports_buffering":                   true,
		"supported_encodings":                  []string{"v1"},
		"force_use_search":                     true,
		"client_reported_search_source":        "conversation_composer_web_icon",
		"client_contextual_info":               map[string]any{"is_dark_mode": false, "time_since_loaded": 36, "page_height": 925, "page_width": 886, "pixel_ratio": 2, "screen_height": 1440, "screen_width": 2560, "app_name": "chatgpt.com"},
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
	}
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	for k, v := range c.headers(account, path, path, c.imageHeaders(requirements, conduit, "text/event-stream")) {
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
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if conversationID == "" {
			conversationID = ExtractConversationID(payload)
		}
		if payload == "[DONE]" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if conversationID == "" {
		return "", fmt.Errorf("conversation_id not found in stream")
	}
	return conversationID, nil
}

func (c *Client) waitSearchResult(ctx context.Context, account accounts.Account, conversationID string, timeout, interval time.Duration) (SearchResult, error) {
	deadline := time.Now().Add(timeout)
	var last SearchResult
	lastAnswer := ""
	stableHits := 0
	for time.Now().Before(deadline) {
		conv, err := c.getSearchConversation(ctx, account, conversationID)
		if err == nil {
			last = extractSearchResult(conversationID, conv)
			if last.Answer != "" {
				if last.Status == "finished_successfully" || last.Status == "finished_partial_completion" {
					return last, nil
				}
				if last.Answer == lastAnswer {
					stableHits++
				} else {
					stableHits = 0
					lastAnswer = last.Answer
				}
				if stableHits >= 2 {
					return last, nil
				}
			}
		}
		if err := c.sleep(ctx, interval); err != nil {
			return last, err
		}
	}
	if last.Answer != "" {
		return last, nil
	}
	return SearchResult{}, fmt.Errorf("timed out waiting for search result: %s", conversationID)
}

func (c *Client) getSearchConversation(ctx context.Context, account accounts.Account, conversationID string) (map[string]any, error) {
	path := "/backend-api/conversation/" + conversationID
	route := "/backend-api/conversation/{conversation_id}"
	var out map[string]any
	err := c.doJSON(ctx, account, http.MethodGet, path, route, nil, map[string]string{"Accept": "*/*", "Referer": c.baseURL + "/c/" + conversationID}, &out)
	return out, err
}

func extractSearchResult(conversationID string, conversation map[string]any) SearchResult {
	assistant := latestAssistantMessage(conversation)
	metadata, _ := assistant["metadata"].(map[string]any)
	finish, _ := metadata["finish_details"].(map[string]any)
	answer := searchMessageText(assistant)
	sources := extractSearchSources(assistant)
	seen := map[string]bool{}
	for _, s := range sources {
		seen[s.URL] = true
	}
	for _, raw := range searchURLRE.FindAllString(answer, -1) {
		url := cleanSearchURL(raw)
		if url != "" && !seen[url] {
			sources = append(sources, SearchSource{URL: url})
			seen[url] = true
		}
	}
	return SearchResult{
		ConversationID:     conversationID,
		Status:             firstNonEmpty(str(finish["type"]), str(metadata["status"]), findStringKey(assistant, "status")),
		Answer:             answer,
		Sources:            sources,
		AssistantMessageID: str(assistant["id"]),
		CreateTime:         floatValue(assistant["create_time"]),
	}
}

func latestAssistantMessage(conversation map[string]any) map[string]any {
	mapping, _ := conversation["mapping"].(map[string]any)
	var best map[string]any
	bestTime := -1.0
	for _, nodeRaw := range mapping {
		node, _ := nodeRaw.(map[string]any)
		message, _ := node["message"].(map[string]any)
		author, _ := message["author"].(map[string]any)
		if str(author["role"]) != "assistant" {
			continue
		}
		ct := floatValue(message["create_time"])
		if best == nil || ct >= bestTime {
			best = message
			bestTime = ct
		}
	}
	if best == nil {
		return map[string]any{}
	}
	return best
}

func searchMessageText(message any) string {
	msg, _ := message.(map[string]any)
	content, _ := msg["content"].(map[string]any)
	parts := []string{}
	if text, ok := content["text"].(string); ok && text != "" {
		parts = append(parts, text)
	}
	if arr, ok := content["parts"].([]any); ok {
		for _, part := range arr {
			switch p := part.(type) {
			case string:
				if strings.TrimSpace(p) != "" {
					parts = append(parts, p)
				}
			case map[string]any:
				for _, key := range []string{"text", "summary", "content"} {
					if v := strings.TrimSpace(str(p[key])); v != "" {
						parts = append(parts, v)
					}
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractSearchSources(payload any) []SearchSource {
	sources := []SearchSource{}
	seen := map[string]bool{}
	for _, obj := range walkMaps(payload) {
		metadata, _ := obj["metadata"].(map[string]any)
		url := cleanSearchURL(firstNonEmpty(str(obj["url"]), str(obj["link"]), str(obj["source_url"]), str(metadata["url"])))
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		sources = append(sources, SearchSource{
			Title:      strings.TrimSpace(firstNonEmpty(str(obj["title"]), str(obj["name"]), str(obj["source"]))),
			URL:        url,
			Snippet:    strings.TrimSpace(firstNonEmpty(str(obj["snippet"]), str(obj["text"]), str(obj["description"]))),
			SourceType: strings.TrimSpace(firstNonEmpty(str(obj["type"]), str(obj["source_type"]))),
		})
	}
	return sources
}

func walkMaps(payload any) []map[string]any {
	switch v := payload.(type) {
	case map[string]any:
		out := []map[string]any{v}
		for _, child := range v {
			out = append(out, walkMaps(child)...)
		}
		return out
	case []any:
		out := []map[string]any{}
		for _, child := range v {
			out = append(out, walkMaps(child)...)
		}
		return out
	default:
		return nil
	}
}

func cleanSearchURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), ".,;，。；")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func floatValue(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}
