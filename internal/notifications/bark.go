package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"imagepool/internal/config"
)

type Result struct {
	OK        bool   `json:"ok"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

func TestBark(ctx context.Context, settings config.BarkNotification, client *http.Client) Result {
	if !settings.Enabled || strings.TrimSpace(settings.DeviceKey) == "" {
		return Result{Error: "Bark push is disabled or missing device_key"}
	}
	if client == nil {
		client = http.DefaultClient
	}
	timeout := settings.TimeoutSecs
	if timeout <= 0 {
		timeout = 10
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	title := "Bark test"
	if prefix := strings.TrimSpace(settings.TitlePrefix); prefix != "" {
		title = prefix + " - " + title
	}
	payload, _ := json.Marshal(map[string]string{
		"device_key": settings.DeviceKey,
		"title":      title,
		"body":       "IMAGE POOL Bark test notification",
		"group":      settings.Group,
		"level":      settings.Level,
	})
	endpoint := strings.TrimRight(strings.TrimSpace(settings.ServerURL), "/") + "/push"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "image-pool-bark/1.0")
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Result{LatencyMS: time.Since(started).Milliseconds(), Error: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	result := Result{Status: resp.StatusCode, LatencyMS: time.Since(started).Milliseconds(), OK: resp.StatusCode >= 200 && resp.StatusCode < 300}
	var reply struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &reply)
	if reply.Code >= 400 {
		result.OK = false
	}
	if !result.OK {
		result.Error = strings.TrimSpace(reply.Message)
		if result.Error == "" {
			result.Error = strings.TrimSpace(string(raw))
		}
		if result.Error == "" {
			result.Error = fmt.Sprintf("Bark returned HTTP %d", resp.StatusCode)
		}
	}
	return result
}
