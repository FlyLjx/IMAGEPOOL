package openaiweb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"imagepool/internal/accounts"
)

func (c *Client) startImageGeneration(ctx context.Context, account accounts.Account, prompt, model string, requirements chatRequirements, conduit string, refs []uploadMeta) (conversationID string, fileIDs []string, sedimentIDs []string, err error) {
	path := "/backend-api/f/conversation"
	content := map[string]any{"content_type": "text", "parts": []any{prompt}}
	metadata := map[string]any{"developer_mode_connector_ids": []any{}, "selected_github_repos": []any{}, "selected_all_github_repos": false, "system_hints": []string{"picture_v2"}, "serialization_metadata": map[string]any{"custom_symbol_offsets": []any{}}}
	if len(refs) > 0 {
		parts := make([]any, 0, len(refs)+1)
		attachments := make([]any, 0, len(refs))
		for _, item := range refs {
			parts = append(parts, map[string]any{"content_type": "image_asset_pointer", "asset_pointer": "file-service://" + item.FileID, "width": item.Width, "height": item.Height, "size_bytes": item.FileSize})
			attachments = append(attachments, map[string]any{"id": item.FileID, "mimeType": item.MIMEType, "name": item.FileName, "size": item.FileSize, "width": item.Width, "height": item.Height})
		}
		parts = append(parts, prompt)
		content = map[string]any{"content_type": "multimodal_text", "parts": parts}
		metadata["attachments"] = attachments
	}
	payload := map[string]any{
		"action":            "next",
		"messages":          []any{map[string]any{"id": c.newID(), "author": map[string]any{"role": "user"}, "create_time": float64(time.Now().UnixNano()) / 1e9, "content": content, "metadata": metadata}},
		"parent_message_id": c.newID(), "model": model, "client_prepare_state": "sent", "timezone_offset_min": -480, "timezone": "Asia/Shanghai",
		"conversation_mode": map[string]any{"kind": "primary_assistant"}, "enable_message_followups": true, "system_hints": []string{"picture_v2"}, "supports_buffering": true, "supported_encodings": []string{"v1"},
		"client_contextual_info":               map[string]any{"is_dark_mode": false, "time_since_loaded": 1200, "page_height": 1072, "page_width": 1724, "pixel_ratio": 1.2, "screen_height": 1440, "screen_width": 2560, "app_name": "chatgpt.com"},
		"paragen_cot_summary_display_override": "allow", "force_parallel_switch": "auto",
	}
	body, _ := json.Marshal(payload)
	streamCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(streamCtx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", nil, nil, err
	}
	for k, v := range c.headers(account, path, path, c.imageHeaders(requirements, conduit, "text/event-stream")) {
		request.Header.Set(k, v)
	}
	resp, err := c.clientFor(account, false).Do(request)
	if err != nil {
		return "", nil, nil, err
	}
	defer resp.Body.Close()
	if err := ensureOK(resp, path); err != nil {
		return "", nil, nil, err
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			if conversationID != "" {
				break
			}
			continue
		}
		if conversationID == "" {
			conversationID = ExtractConversationID(payload)
		}
		var v any
		if json.Unmarshal([]byte(payload), &v) == nil {
			var f, s []string
			if len(refs) > 0 {
				f, s = ExtractGeneratedImageReferenceIDs(v)
			} else {
				f, s = ExtractImageReferenceIDs(v)
			}
			fileIDs = appendUnique(fileIDs, f...)
			sedimentIDs = appendUnique(sedimentIDs, s...)
		}
		if conversationID != "" {
			break
		}
	}
	if err := scanner.Err(); err != nil && conversationID == "" {
		return "", nil, nil, err
	}
	if conversationID == "" {
		return "", nil, nil, fmt.Errorf("conversation_id not found in stream")
	}
	return conversationID, fileIDs, sedimentIDs, nil
}

func appendUnique(dst []string, values ...string) []string {
	seen := map[string]bool{}
	for _, item := range dst {
		seen[item] = true
	}
	for _, value := range values {
		if value != "" && !seen[value] {
			dst = append(dst, value)
			seen[value] = true
		}
	}
	return dst
}

func idSet(values []string) map[string]bool {
	set := map[string]bool{}
	for _, value := range values {
		if value != "" {
			set[value] = true
		}
	}
	return set
}

func filterExcludedIDs(values []string, excluded map[string]bool) []string {
	if len(values) == 0 || len(excluded) == 0 {
		return append([]string(nil), values...)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !excluded[value] {
			out = append(out, value)
		}
	}
	return out
}

func (c *Client) ResolveConversationImageURLs(ctx context.Context, account accounts.Account, conversationID string, fileIDs, sedimentIDs []string, poll bool, excludeFileIDs ...string) ([]string, error) {
	return c.resolveConversationImageURLs(ctx, account, conversationID, fileIDs, sedimentIDs, poll, nil, excludeFileIDs...)
}

func (c *Client) resolveConversationImageURLs(ctx context.Context, account accounts.Account, conversationID string, fileIDs, sedimentIDs []string, poll bool, progress func(ProgressEvent), excludeFileIDs ...string) ([]string, error) {
	excluded := idSet(excludeFileIDs)
	fileIDs = filterExcludedIDs(fileIDs, excluded)
	if poll && conversationID != "" {
		f, s, err := c.pollImageResultsWithProgress(ctx, account, conversationID, fileIDs, sedimentIDs, progress, excluded)
		if err != nil {
			if len(fileIDs) == 0 && len(sedimentIDs) == 0 {
				return nil, err
			}
		} else {
			fileIDs = appendUnique(fileIDs, filterExcludedIDs(f, excluded)...)
			sedimentIDs = appendUnique(sedimentIDs, s...)
		}
	}
	return c.resolveImageURLs(ctx, account, conversationID, filterExcludedIDs(fileIDs, excluded), sedimentIDs)
}

func (c *Client) pollImageResults(ctx context.Context, account accounts.Account, conversationID string, initialFileIDs, initialSedimentIDs []string, excludedFileIDsArg ...map[string]bool) ([]string, []string, error) {
	return c.pollImageResultsWithProgress(ctx, account, conversationID, initialFileIDs, initialSedimentIDs, nil, excludedFileIDsArg...)
}

func (c *Client) pollImageResultsWithProgress(ctx context.Context, account accounts.Account, conversationID string, initialFileIDs, initialSedimentIDs []string, progress func(ProgressEvent), excludedFileIDsArg ...map[string]bool) ([]string, []string, error) {
	excludedFileIDs := map[string]bool{}
	if len(excludedFileIDsArg) > 0 && excludedFileIDsArg[0] != nil {
		excludedFileIDs = excludedFileIDsArg[0]
	}
	fileIDs := filterExcludedIDs(initialFileIDs, excludedFileIDs)
	sedimentIDs := append([]string(nil), initialSedimentIDs...)
	startedAt := time.Now()
	deadline := startedAt.Add(c.pollTimeout)
	if len(fileIDs) == 0 && len(sedimentIDs) == 0 && c.pollInitialWait > 0 {
		if err := c.sleep(ctx, c.pollInitialWait); err != nil {
			return nil, nil, err
		}
	}
	lastHeartbeat := startedAt
	reportHeartbeat := func() {
		interval := c.pollHeartbeatInterval
		if interval <= 0 {
			interval = 15 * time.Second
		}
		now := time.Now()
		if progress == nil || now.Sub(lastHeartbeat) < interval {
			return
		}
		elapsed := int(now.Sub(startedAt).Round(time.Second).Seconds())
		if elapsed < 1 {
			elapsed = 1
		}
		progress(ProgressEvent{
			Progress: "polling_image",
			Message:  fmt.Sprintf("图片仍在生成，已等待 %d 秒", elapsed),
			Details:  map[string]any{"conversation_id": conversationID, "elapsed_secs": elapsed},
		})
		lastHeartbeat = now
	}
	lastHit := ""
	for time.Now().Before(deadline) {
		reportHeartbeat()
		pollCtx, cancel := c.pollRequestContext(ctx, deadline)
		conversation, err := c.getConversation(pollCtx, account, conversationID)
		cancel()
		reportHeartbeat()
		if err != nil {
			// Fresh image conversations can return conversation_inaccessible before
			// their document is available for polling. Keep the same account and
			// conversation ID; switching accounts cannot access this conversation.
			if IsConversationInaccessibleError(err) || isRetryableImagePollError(err) {
				_ = c.sleep(ctx, c.pollInterval)
				continue
			}
			return nil, nil, err
		}
		var f, s []string
		if len(excludedFileIDs) > 0 {
			f, s = ExtractGeneratedImageReferenceIDs(conversation)
		} else {
			f, s = ExtractImageReferenceIDs(conversation)
		}
		f = filterExcludedIDs(f, excludedFileIDs)
		fileIDs = appendUnique(fileIDs, f...)
		sedimentIDs = appendUnique(sedimentIDs, s...)
		if len(fileIDs) > 0 || len(sedimentIDs) > 0 {
			hit := strings.Join(fileIDs, ",") + "|" + strings.Join(sedimentIDs, ",")
			if !c.checkBeforeHit || !c.settleEnabled || lastHit == hit {
				return fileIDs, sedimentIDs, nil
			}
			lastHit = hit
			if err := c.sleep(ctx, c.settle); err != nil {
				return nil, nil, err
			}
			continue
		}
		if policy := findContentPolicyText(conversation); policy != "" {
			return nil, nil, fmt.Errorf("%w: %s", ErrContentPolicy, policy)
		}
		if err := c.sleep(ctx, c.pollInterval); err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, fmt.Errorf("%w: ChatGPT 生图超时（已等待 %.0f 秒）", ErrPollTimeout, c.pollTimeout.Seconds())
}

func (c *Client) pollRequestContext(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	timeout := c.pollRequestTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if c.timeout > 0 && c.timeout < timeout {
		timeout = c.timeout
	}
	if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
		timeout = remaining
	}
	return context.WithTimeout(ctx, timeout)
}

func isRetryableImagePollError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary()) {
		return true
	}
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return upstream.StatusCode == http.StatusRequestTimeout || upstream.StatusCode == http.StatusTooManyRequests || upstream.StatusCode >= http.StatusInternalServerError
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "timeout") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "temporarily unavailable")
}

func (c *Client) getConversation(ctx context.Context, account accounts.Account, conversationID string) (map[string]any, error) {
	path := "/backend-api/conversation/" + conversationID
	var out map[string]any
	err := c.doJSON(ctx, account, http.MethodGet, path, path, nil, map[string]string{"Accept": "application/json"}, &out)
	return out, err
}

func findContentPolicyText(v any) string {
	switch x := v.(type) {
	case map[string]any:
		text := ""
		if content, ok := x["content"].(map[string]any); ok {
			if parts, ok := content["parts"].([]any); ok {
				for _, part := range parts {
					if s, ok := part.(string); ok {
						text += s + "\n"
					}
				}
			}
			text += str(content["text"])
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, "content policy") || strings.Contains(lower, "moderation") || strings.Contains(text, "内容政策") || strings.Contains(text, "防护限制") {
			return strings.TrimSpace(text)
		}
		for _, child := range x {
			if got := findContentPolicyText(child); got != "" {
				return got
			}
		}
	case []any:
		for _, child := range x {
			if got := findContentPolicyText(child); got != "" {
				return got
			}
		}
	}
	return ""
}

func (c *Client) resolveImageURLs(ctx context.Context, account accounts.Account, conversationID string, fileIDs, sedimentIDs []string) ([]string, error) {
	urls := []string{}
	for _, id := range fileIDs {
		if id == "file_upload" {
			continue
		}
		path := "/backend-api/files/" + id + "/download"
		var out map[string]any
		if err := c.doJSON(ctx, account, http.MethodGet, path, path, nil, map[string]string{"Accept": "application/json"}, &out); err != nil {
			continue
		}
		urls = appendUnique(urls, str(out["download_url"]), str(out["url"]))
	}
	for _, id := range sedimentIDs {
		if conversationID == "" {
			continue
		}
		path := "/backend-api/conversation/" + conversationID + "/attachment/" + id + "/download"
		var out map[string]any
		if err := c.doJSON(ctx, account, http.MethodGet, path, path, nil, map[string]string{"Accept": "application/json"}, &out); err != nil {
			continue
		}
		urls = appendUnique(urls, str(out["download_url"]), str(out["url"]))
	}
	return urls, nil
}

func (c *Client) uploadImage(ctx context.Context, account accounts.Account, image ImageInput, index, total int) (uploadMeta, error) {
	if image.Width == 0 || image.Height == 0 || image.MIMEType == "" {
		normalized, err := ImageInputFromBytes(image.FileName, image.MIMEType, image.Data)
		if err != nil {
			return uploadMeta{}, err
		}
		image = normalized
	}
	path := "/backend-api/files"
	var created struct {
		FileID    string `json:"file_id"`
		UploadURL string `json:"upload_url"`
	}
	body := map[string]any{"file_name": image.FileName, "file_size": len(image.Data), "use_case": "multimodal", "width": image.Width, "height": image.Height}
	if err := c.doJSON(ctx, account, http.MethodPost, path, path, body, map[string]string{"Content-Type": "application/json", "Accept": "application/json"}, &created); err != nil {
		return uploadMeta{}, err
	}
	if created.FileID == "" || created.UploadURL == "" {
		return uploadMeta{}, fmt.Errorf("invalid upload response")
	}
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, created.UploadURL, bytes.NewReader(image.Data))
	if err != nil {
		return uploadMeta{}, err
	}
	putReq.Header.Set("Content-Type", image.MIMEType)
	putReq.Header.Set("x-ms-blob-type", "BlockBlob")
	putReq.Header.Set("x-ms-version", "2020-04-08")
	putReq.Header.Set("Origin", c.baseURL)
	putReq.Header.Set("Referer", c.baseURL+"/")
	putReq.Header.Set("User-Agent", c.userAgent(account))
	resp, err := c.clientFor(account, true).Do(putReq)
	if err != nil {
		return uploadMeta{}, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err := ensureOK(resp, "image_upload"); err != nil {
		return uploadMeta{}, err
	}
	confirmPath := "/backend-api/files/" + created.FileID + "/uploaded"
	if err := c.doJSON(ctx, account, http.MethodPost, confirmPath, confirmPath, map[string]any{}, map[string]string{"Content-Type": "application/json", "Accept": "application/json"}, nil); err != nil {
		return uploadMeta{}, err
	}
	return uploadMeta{FileID: created.FileID, FileName: image.FileName, FileSize: len(image.Data), MIMEType: image.MIMEType, Width: image.Width, Height: image.Height}, nil
}

func (c *Client) headers(account accounts.Account, path, route string, extra map[string]string) map[string]string {
	h := c.baseHeaders(account)
	h["X-OpenAI-Target-Path"] = path
	if route == "" {
		route = path
	}
	h["X-OpenAI-Target-Route"] = route
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func (c *Client) imageHeaders(req chatRequirements, conduit, accept string) map[string]string {
	h := map[string]string{"Content-Type": "application/json", "Accept": accept, "OpenAI-Sentinel-Chat-Requirements-Token": req.Token}
	if req.ProofToken != "" {
		h["OpenAI-Sentinel-Proof-Token"] = req.ProofToken
	}
	if req.TurnstileToken != "" {
		h["OpenAI-Sentinel-Turnstile-Token"] = req.TurnstileToken
	}
	if req.SOToken != "" {
		h["OpenAI-Sentinel-SO-Token"] = req.SOToken
	}
	if conduit != "" {
		h["X-Conduit-Token"] = conduit
	}
	if accept == "text/event-stream" {
		h["X-Oai-Turn-Trace-Id"] = c.newID()
	}
	return h
}

func (c *Client) baseHeaders(account accounts.Account) map[string]string {
	device := account.DeviceID
	if device == "" && account.FP != nil {
		device = account.FP["oai-device-id"]
	}
	if device == "" {
		device = c.newID()
	}
	session := account.SessionID
	if session == "" && account.FP != nil {
		session = account.FP["oai-session-id"]
	}
	if session == "" {
		session = c.newID()
	}
	secCHUA := accounts.DefaultBrowserSecCHUA
	if account.FP != nil && account.FP["sec-ch-ua"] != "" {
		secCHUA = account.FP["sec-ch-ua"]
	}
	h := map[string]string{"User-Agent": c.userAgent(account), "Origin": c.baseURL, "Referer": c.baseURL + "/", "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8,en-US;q=0.7", "Cache-Control": "no-cache", "Pragma": "no-cache", "Sec-Ch-Ua": secCHUA, "Sec-Ch-Ua-Mobile": "?0", "Sec-Ch-Ua-Platform": `"Windows"`, "Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin", "OAI-Device-Id": device, "OAI-Session-Id": session, "OAI-Language": "zh-CN", "OAI-Client-Version": defaultClientVersion, "OAI-Client-Build-Number": defaultClientBuildNumber}
	if account.AccessToken != "" {
		h["Authorization"] = "Bearer " + account.AccessToken
	}
	if cookie := c.clearanceCookie(); cookie != "" {
		h["Cookie"] = cookie
	}
	return h
}

func (c *Client) userAgent(account accounts.Account) string {
	if account.UserAgent != "" {
		return account.UserAgent
	}
	if account.FP != nil && account.FP["user-agent"] != "" {
		return account.FP["user-agent"]
	}
	if c.proxyRuntime.Enabled && c.proxyRuntime.Clearance.Enabled && c.proxyRuntime.Clearance.UserAgent != "" {
		return c.proxyRuntime.Clearance.UserAgent
	}
	return defaultUserAgent
}

func (c *Client) clearanceCookie() string {
	runtime := c.proxyRuntime
	if !runtime.Enabled || !runtime.Clearance.Enabled || runtime.Clearance.Mode == "none" {
		return ""
	}
	cookie := strings.TrimSpace(runtime.Clearance.CFCookies)
	clearance := strings.TrimSpace(runtime.Clearance.CFClearance)
	if clearance == "" || strings.Contains(strings.ToLower(cookie), "cf_clearance=") {
		return cookie
	}
	if cookie == "" {
		return "cf_clearance=" + clearance
	}
	return strings.TrimRight(cookie, "; ") + "; cf_clearance=" + clearance
}
