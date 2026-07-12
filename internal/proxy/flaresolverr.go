package proxy

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

type ClearanceSolution struct {
	Cookies   string `json:"cookies"`
	Clearance string `json:"cf_clearance"`
	UserAgent string `json:"user_agent"`
}

// SolveFlareSolverr requests a fresh ChatGPT clearance bundle from the configured solver.
func SolveFlareSolverr(ctx context.Context, runtime config.ProxyRuntime, targetURL string) (ClearanceSolution, error) {
	clearance := runtime.Clearance
	endpoint := strings.TrimRight(strings.TrimSpace(clearance.FlareSolverrURL), "/")
	if !runtime.Enabled || !clearance.Enabled || clearance.Mode != "flaresolverr" || endpoint == "" {
		return ClearanceSolution{}, fmt.Errorf("flaresolverr clearance is not enabled")
	}
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		targetURL = "https://chatgpt.com"
	}
	timeout := clearance.TimeoutSec
	if timeout <= 0 {
		timeout = 60
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"cmd": "request.get", "url": targetURL, "maxTimeout": timeout * 1000})
	client, err := NewHTTPClientForRuntime(runtime, time.Duration(timeout)*time.Second)
	if err != nil {
		return ClearanceSolution{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1", bytes.NewReader(payload))
	if err != nil {
		return ClearanceSolution{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ClearanceSolution{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			UserAgent string `json:"userAgent"`
			Cookies   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"cookies"`
		} `json:"solution"`
	}
	_ = json.Unmarshal(raw, &result)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !strings.EqualFold(result.Status, "ok") {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return ClearanceSolution{}, fmt.Errorf("flaresolverr failed (HTTP %d): %s", resp.StatusCode, message)
	}
	cookies := make([]string, 0, len(result.Solution.Cookies))
	solution := ClearanceSolution{UserAgent: strings.TrimSpace(result.Solution.UserAgent)}
	for _, item := range result.Solution.Cookies {
		name, value := strings.TrimSpace(item.Name), strings.TrimSpace(item.Value)
		if name == "" {
			continue
		}
		cookies = append(cookies, name+"="+value)
		if name == "cf_clearance" {
			solution.Clearance = value
		}
	}
	solution.Cookies = strings.Join(cookies, "; ")
	if solution.Clearance == "" && solution.Cookies == "" {
		return ClearanceSolution{}, fmt.Errorf("flaresolverr returned no cookies")
	}
	return solution, nil
}
