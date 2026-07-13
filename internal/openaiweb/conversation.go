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

const (
	maxSSEDataLineSize     = 16 * 1024 * 1024
	maxImageStartAttempts  = 3
	maxImageResumeAttempts = 1
)

type imageStreamState struct {
	conversationID string
	resumeToken    string
	offset         int
	fileIDs        []string
	sedimentIDs    []string
}

func (c *Client) startImageGeneration(ctx context.Context, account accounts.Account, prompt, model string, requirements chatRequirements, conduit, turnTraceID, parentMessageID string, refs []uploadMeta) (conversationID string, fileIDs []string, sedimentIDs []string, err error) {
	path := "/backend-api/f/conversation"
	if parentMessageID == "" {
		parentMessageID = c.newID()
	}
	content := imageMessageContent(prompt, refs)
	metadata := map[string]any{"developer_mode_connector_ids": []any{}, "selected_github_repos": []any{}, "selected_all_github_repos": false, "system_hints": []string{"picture_v2"}, "serialization_metadata": map[string]any{"custom_symbol_offsets": []any{}}}
	if len(refs) > 0 {
		attachments := make([]any, 0, len(refs))
		for _, item := range refs {
			attachments = append(attachments, map[string]any{"id": item.FileID, "mimeType": item.MIMEType, "name": item.FileName, "size": item.FileSize, "width": item.Width, "height": item.Height})
		}
		metadata["attachments"] = attachments
	}
	payload := map[string]any{
		"action":            "next",
		"messages":          []any{map[string]any{"id": c.newID(), "author": map[string]any{"role": "user"}, "create_time": float64(time.Now().UnixNano()) / 1e9, "content": content, "metadata": metadata}},
		"parent_message_id": parentMessageID, "model": model, "client_prepare_state": "success", "timezone_offset_min": -480, "timezone": "Asia/Shanghai",
		"conversation_mode": map[string]any{"kind": "primary_assistant"}, "enable_message_followups": true, "system_hints": []string{"picture_v2"}, "supports_buffering": true, "supported_encodings": []string{"v1"},
		"client_contextual_info":               map[string]any{"is_dark_mode": false, "time_since_loaded": 1200, "page_height": 1072, "page_width": 1724, "pixel_ratio": 1.2, "screen_height": 1440, "screen_width": 2560, "app_name": "chatgpt.com"},
		"paragen_cot_summary_display_override": "allow", "force_parallel_switch": "auto", "thinking_effort": "standard",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, nil, err
	}
	headers := c.headers(account, path, path, c.imageHeaders(requirements, conduit, "text/event-stream", turnTraceID))
	headers["OAI-Echo-Logs"] = "0,943,1,65876,0,68124,1,68930"
	headers["OAI-Telemetry"] = "[1,null]"
	resp, err := c.openImageGenerationStream(ctx, account, path, body, headers)
	if err != nil {
		return "", nil, nil, err
	}
	defer resp.Body.Close()
	state := &imageStreamState{}
	foundReferences, streamDone, streamErr := c.consumeImageStream(ctx, resp.Body, refs, state)
	if streamErr != nil {
		if ctx.Err() != nil {
			return state.conversationID, state.fileIDs, state.sedimentIDs, ctx.Err()
		}
		if state.conversationID == "" || !isRetryableResumeError(streamErr) {
			return state.conversationID, state.fileIDs, state.sedimentIDs, streamErr
		}
	}
	if state.conversationID == "" {
		return "", nil, nil, fmt.Errorf("conversation_id not found in stream")
	}
	if foundReferences || streamDone || state.resumeToken == "" {
		return state.conversationID, state.fileIDs, state.sedimentIDs, nil
	}

	for attempt := 0; attempt < maxImageResumeAttempts && ctx.Err() == nil; attempt++ {
		resumeBody, resumeErr := c.resumeImageGeneration(ctx, account, turnTraceID, state)
		if resumeErr != nil {
			if isResumePollingFallback(resumeErr) {
				break
			}
			if !isRetryableResumeError(resumeErr) {
				return state.conversationID, state.fileIDs, state.sedimentIDs, resumeErr
			}
			if attempt+1 >= maxImageResumeAttempts {
				break
			}
			if err := c.sleep(ctx, imageResumeRetryDelay(attempt)); err != nil {
				return state.conversationID, state.fileIDs, state.sedimentIDs, err
			}
			continue
		}
		foundReferences, streamDone, streamErr = c.consumeImageStream(ctx, resumeBody, refs, state)
		if streamErr != nil {
			if ctx.Err() != nil {
				return state.conversationID, state.fileIDs, state.sedimentIDs, ctx.Err()
			}
			if !isRetryableResumeError(streamErr) {
				return state.conversationID, state.fileIDs, state.sedimentIDs, streamErr
			}
		}
		if foundReferences || streamDone {
			break
		}
	}
	if ctx.Err() != nil {
		return state.conversationID, state.fileIDs, state.sedimentIDs, ctx.Err()
	}
	return state.conversationID, state.fileIDs, state.sedimentIDs, nil
}

func (c *Client) openImageGenerationStream(ctx context.Context, account accounts.Account, path string, body []byte, headers map[string]string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < maxImageStartAttempts && ctx.Err() == nil; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		resp, err := c.clientFor(account, false).Do(request)
		if err == nil {
			if statusErr := ensureOK(resp, path); statusErr == nil {
				return resp, nil
			} else {
				lastErr = statusErr
				resp.Body.Close()
			}
		} else {
			lastErr = err
		}
		if !isRetryableResumeError(lastErr) {
			return nil, lastErr
		}
		if attempt+1 >= maxImageStartAttempts {
			return nil, lastErr
		}
		if err := c.sleep(ctx, imageResumeRetryDelay(attempt)); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, lastErr
}

func (c *Client) consumeImageStream(ctx context.Context, body io.ReadCloser, refs []uploadMeta, state *imageStreamState) (foundReferences, streamDone bool, err error) {
	defer body.Close()
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	payloads := make(chan string, 1)
	readErrors := make(chan error, 1)
	go func() {
		readErrors <- forEachSSEData(body, func(payload string) bool {
			select {
			case payloads <- payload:
				return true
			case <-readCtx.Done():
				return false
			}
		})
		close(payloads)
	}()

	idleTimeout := c.imageStreamIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 60 * time.Second
	}
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()
	for {
		select {
		case payload, ok := <-payloads:
			if !ok {
				return false, false, <-readErrors
			}
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleTimeout)
			payload = strings.TrimSpace(payload)
			if payload == "" || payload == `"v1"` {
				continue
			}
			if payload == "[DONE]" {
				return false, true, nil
			}
			var value any
			if json.Unmarshal([]byte(payload), &value) != nil {
				continue
			}
			if token, conversationID, ok := extractResumeConversationToken(value); ok {
				state.resumeToken = token
				if state.conversationID == "" {
					state.conversationID = conversationID
				}
				continue
			}
			if state.conversationID == "" {
				state.conversationID = ExtractConversationID(payload)
			}
			var fileIDs, sedimentIDs []string
			if len(refs) > 0 {
				fileIDs, sedimentIDs = ExtractGeneratedImageReferenceIDs(value)
			} else {
				fileIDs, sedimentIDs = ExtractImageReferenceIDs(value)
			}
			state.fileIDs = appendUnique(state.fileIDs, fileIDs...)
			state.sedimentIDs = appendUnique(state.sedimentIDs, sedimentIDs...)
			state.offset++
			if len(state.fileIDs) > 0 || len(state.sedimentIDs) > 0 {
				return true, false, nil
			}
		case <-idleTimer.C:
			cancel()
			_ = body.Close()
			return false, false, nil
		case <-ctx.Done():
			cancel()
			_ = body.Close()
			return false, false, ctx.Err()
		}
	}
}

func extractResumeConversationToken(value any) (token, conversationID string, ok bool) {
	item, _ := value.(map[string]any)
	if str(item["type"]) != "resume_conversation_token" {
		return "", "", false
	}
	token = strings.TrimSpace(str(item["token"]))
	conversationID = strings.TrimSpace(str(item["conversation_id"]))
	return token, conversationID, token != "" && conversationID != ""
}

func (c *Client) resumeImageGeneration(ctx context.Context, account accounts.Account, turnTraceID string, state *imageStreamState) (io.ReadCloser, error) {
	path := "/backend-api/f/conversation/resume"
	body, err := json.Marshal(map[string]any{"conversation_id": state.conversationID, "offset": state.offset})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	extra := map[string]string{"Accept": "text/event-stream", "Content-Type": "application/json", "X-Conduit-Token": state.resumeToken}
	if turnTraceID != "" {
		extra["X-Oai-Turn-Trace-Id"] = turnTraceID
	}
	for key, value := range c.headers(account, path, path, extra) {
		request.Header.Set(key, value)
	}
	resp, err := c.clientFor(account, false).Do(request)
	if err != nil {
		return nil, err
	}
	if err := ensureOK(resp, path); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

func isRetryableResumeError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		return true
	}
	switch upstream.StatusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isResumePollingFallback(err error) bool {
	var upstream *UpstreamError
	return errors.As(err, &upstream) && upstream.StatusCode == http.StatusNotFound
}

func imageResumeRetryDelay(attempt int) time.Duration {
	delay := 300 * time.Millisecond
	for index := 0; index < attempt && delay < 5*time.Second; index++ {
		delay = time.Duration(float64(delay) * 1.5)
	}
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

// forEachSSEData implements the SSE framing rules needed by ChatGPT's stream:
// a single event can contain multiple data lines, and events are separated by
// a blank line. Scanner also reassembles HTTP chunks that split a line.
func forEachSSEData(body io.Reader, handle func(string) bool) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEDataLineSize)
	dataLines := []string{}
	dispatch := func() bool {
		if len(dataLines) == 0 {
			return true
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		return handle(payload)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if !dispatch() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasSeparator := strings.Cut(line, ":")
		if field != "data" {
			continue
		}
		if hasSeparator {
			value = strings.TrimPrefix(value, " ")
		}
		dataLines = append(dataLines, value)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_ = dispatch()
	return nil
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
	var initialResolveErr error
	if len(fileIDs) > 0 || len(sedimentIDs) > 0 {
		if urls, err := c.resolveImageURLs(ctx, account, conversationID, fileIDs, sedimentIDs); len(urls) > 0 {
			return urls, nil
		} else {
			initialResolveErr = err
		}
	}
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
	urls, err := c.resolveImageURLs(ctx, account, conversationID, filterExcludedIDs(fileIDs, excluded), sedimentIDs)
	if err != nil {
		return nil, err
	}
	if len(urls) == 0 && initialResolveErr != nil {
		return nil, fmt.Errorf("image stream references could not be resolved: %w", initialResolveErr)
	}
	return urls, nil
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
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "unexpected eof") ||
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
	var failures []error
	for _, id := range fileIDs {
		if id == "file_upload" {
			continue
		}
		path := "/backend-api/files/" + id + "/download"
		var out map[string]any
		if err := c.doJSON(ctx, account, http.MethodGet, path, path, nil, map[string]string{"Accept": "application/json"}, &out); err != nil {
			failures = append(failures, fmt.Errorf("resolve file %s: %w", id, err))
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
			failures = append(failures, fmt.Errorf("resolve attachment %s: %w", id, err))
			continue
		}
		urls = appendUnique(urls, str(out["download_url"]), str(out["url"]))
	}
	if len(urls) == 0 && len(failures) > 0 {
		return nil, errors.Join(failures...)
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

func imageMessageContent(prompt string, refs []uploadMeta) map[string]any {
	if len(refs) == 0 {
		return map[string]any{"content_type": "text", "parts": []any{prompt}}
	}
	parts := make([]any, 0, len(refs)+1)
	for _, item := range refs {
		parts = append(parts, map[string]any{"content_type": "image_asset_pointer", "asset_pointer": "file-service://" + item.FileID, "width": item.Width, "height": item.Height, "size_bytes": item.FileSize})
	}
	parts = append(parts, prompt)
	return map[string]any{"content_type": "multimodal_text", "parts": parts}
}

func imageAttachmentMIMETypes(refs []uploadMeta) []string {
	seen := make(map[string]bool, len(refs))
	result := make([]string, 0, len(refs))
	for _, item := range refs {
		mimeType := strings.TrimSpace(item.MIMEType)
		if mimeType != "" && !seen[mimeType] {
			seen[mimeType] = true
			result = append(result, mimeType)
		}
	}
	return result
}

func (c *Client) imageHeaders(req chatRequirements, conduit, accept, turnTraceID string) map[string]string {
	h := map[string]string{"Content-Type": "application/json", "Accept": accept, "OpenAI-Sentinel-Chat-Requirements-Token": req.Token}
	if req.PrepareToken != "" {
		h["OpenAI-Sentinel-Chat-Requirements-Prepare-Token"] = req.PrepareToken
	}
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
	if turnTraceID != "" {
		h["X-Oai-Turn-Trace-Id"] = turnTraceID
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
