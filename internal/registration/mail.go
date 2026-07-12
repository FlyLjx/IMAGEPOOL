package registration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const defaultTempMailURL = "https://api.tempmail.lol/v2"

var otpRE = regexp.MustCompile(`\d{6}`)

type Mailbox struct {
	Provider string
	Address  string
	Token    string
	BaseURL  string
}

type MailboxProvider interface {
	Create(context.Context, Mail) (Mailbox, error)
	WaitForCode(context.Context, Mail, Mailbox) (string, error)
}

type tempMailProvider struct {
	client *http.Client
	next   *atomic.Uint64
}

func newTempMailProvider(client *http.Client, selectors ...*atomic.Uint64) MailboxProvider {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	selector := &atomic.Uint64{}
	if len(selectors) > 0 && selectors[0] != nil {
		selector = selectors[0]
	}
	return tempMailProvider{client: client, next: selector}
}

func (p tempMailProvider) Create(ctx context.Context, settings Mail) (Mailbox, error) {
	index := uint64(0)
	if p.next != nil {
		index = p.next.Add(1) - 1
	}
	entry, err := enabledProviderAt(settings.Providers, index)
	if err != nil {
		return Mailbox{}, err
	}
	if strings.TrimSpace(stringValue(entry, "type")) != "tempmail_lol" {
		return Mailbox{}, fmt.Errorf("unsupported mail provider %q; IMAGE POOL currently supports tempmail_lol", stringValue(entry, "type"))
	}
	baseURL := strings.TrimRight(strings.TrimSpace(stringValue(entry, "api_base")), "/")
	if baseURL == "" {
		baseURL = defaultTempMailURL
	}
	payload := map[string]any{}
	if domain := strings.TrimSpace(stringValue(entry, "domain")); domain != "" {
		payload["domain"] = domain
	}
	if prefix := strings.TrimSpace(stringValue(entry, "prefix")); prefix != "" {
		payload["prefix"] = prefix
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/inbox/create", strings.NewReader(string(body)))
	if err != nil {
		return Mailbox{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(stringValue(entry, "api_key")); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	var response struct {
		Address string `json:"address"`
		Token   string `json:"token"`
	}
	if err := decodeOK(p.client, req, &response); err != nil {
		return Mailbox{}, fmt.Errorf("create temp mailbox: %w", err)
	}
	if strings.TrimSpace(response.Address) == "" || strings.TrimSpace(response.Token) == "" {
		return Mailbox{}, fmt.Errorf("create temp mailbox: missing address or token")
	}
	return Mailbox{Provider: "tempmail_lol", Address: strings.TrimSpace(response.Address), Token: strings.TrimSpace(response.Token), BaseURL: baseURL}, nil
}

func (p tempMailProvider) WaitForCode(ctx context.Context, settings Mail, mailbox Mailbox) (string, error) {
	if mailbox.Token == "" {
		return "", fmt.Errorf("mailbox token is empty")
	}
	baseURL := mailbox.BaseURL
	if baseURL == "" {
		baseURL = defaultTempMailURL
	}
	wait := time.Duration(settings.WaitTimeout) * time.Second
	if wait <= 0 {
		wait = 120 * time.Second
	}
	interval := time.Duration(settings.WaitInterval) * time.Second
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		u, _ := url.Parse(baseURL + "/inbox")
		q := u.Query()
		q.Set("token", mailbox.Token)
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", err
		}
		var response struct {
			Emails   []map[string]any `json:"emails"`
			Messages []map[string]any `json:"messages"`
		}
		if err := decodeOK(p.client, req, &response); err != nil {
			return "", fmt.Errorf("poll temp mailbox: %w", err)
		}
		for _, message := range append(response.Emails, response.Messages...) {
			code := extractOTP(strings.Join([]string{stringValue(message, "body"), stringValue(message, "text"), stringValue(message, "html"), stringValue(message, "subject")}, "\n"))
			if code != "" {
				return code, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("waiting for email verification code timed out")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}
}

func enabledProvider(providers []map[string]any) (map[string]any, error) {
	return enabledProviderAt(providers, 0)
}

func enabledProviderAt(providers []map[string]any, index uint64) (map[string]any, error) {
	enabledProviders := make([]map[string]any, 0, len(providers))
	for _, provider := range providers {
		enabled, exists := provider["enabled"]
		if !exists {
			enabled = provider["enable"]
		}
		if value, ok := enabled.(bool); ok && value {
			enabledProviders = append(enabledProviders, provider)
		}
	}
	if len(enabledProviders) > 0 {
		return enabledProviders[index%uint64(len(enabledProviders))], nil
	}
	return nil, fmt.Errorf("mail.providers has no enabled provider")
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key]
	return strings.TrimSpace(fmt.Sprint(value))
}

func extractOTP(text string) string {
	for _, match := range otpRE.FindAllStringSubmatch(text, -1) {
		if len(match) == 1 && match[0] != "177010" {
			return match[0]
		}
	}
	return ""
}
