package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	callbackTimeout     = 15 * time.Second
	callbackMaxAttempts = 3
)

type callbackSnapshot struct {
	Delivered      int        `json:"delivered"`
	Failed         int        `json:"failed"`
	Attempts       int        `json:"attempts"`
	LastError      string     `json:"last_error,omitempty"`
	LastDeliveryAt *time.Time `json:"last_delivery_at,omitempty"`
}

type callbackDispatcher struct {
	client *http.Client
	mu     sync.RWMutex
	stats  callbackSnapshot
}

func newCallbackDispatcher() *callbackDispatcher {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = publicCallbackDialContext
	client := &http.Client{
		Timeout:   callbackTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("callback redirect limit exceeded")
			}
			return validateCallbackURL(req.Context(), req.URL.String())
		},
	}
	return &callbackDispatcher{client: client}
}

func validateCallbackURL(ctx context.Context, raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid callback_url: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("callback_url must be a public HTTPS URL")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("callback_url must not contain a fragment")
	}
	_, err = resolvePublicCallbackIPs(ctx, parsed.Hostname())
	if err != nil {
		return fmt.Errorf("invalid callback_url: %w", err)
	}
	return nil
}

func publicCallbackDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := resolvePublicCallbackIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range ips {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func resolvePublicCallbackIPs(ctx context.Context, host string) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, strings.TrimSpace(host))
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("callback host did not resolve")
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if !isPublicCallbackIP(address.IP) {
			return nil, fmt.Errorf("callback host resolves to a non-public address")
		}
		ips = append(ips, address.IP)
	}
	return ips, nil
}

func isPublicCallbackIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	blocked := []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	for _, prefix := range blocked {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func (d *callbackDispatcher) deliver(rawURL string, payload any) {
	if d == nil || strings.TrimSpace(rawURL) == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		d.record(false, err)
		return
	}
	var lastErr error
	for attempt := 1; attempt <= callbackMaxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), callbackTimeout)
		if err := validateCallbackURL(ctx, rawURL); err != nil {
			cancel()
			d.record(false, err)
			return
		}
		d.recordAttempt()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "IMAGE-POOL-Callback/1.0")
			var resp *http.Response
			resp, err = d.client.Do(req)
			if resp != nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
				_ = resp.Body.Close()
				if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
					cancel()
					d.record(true, nil)
					return
				}
				if err == nil {
					err = fmt.Errorf("callback returned HTTP %d", resp.StatusCode)
				}
			}
		}
		cancel()
		lastErr = err
		if attempt < callbackMaxAttempts {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
	}
	d.record(false, lastErr)
}

func (d *callbackDispatcher) recordAttempt() {
	d.mu.Lock()
	d.stats.Attempts++
	d.mu.Unlock()
}

func (d *callbackDispatcher) record(success bool, err error) {
	now := time.Now()
	d.mu.Lock()
	d.stats.LastDeliveryAt = &now
	if success {
		d.stats.Delivered++
		d.stats.LastError = ""
	} else {
		d.stats.Failed++
		if err != nil {
			d.stats.LastError = err.Error()
		}
	}
	d.mu.Unlock()
}

func (d *callbackDispatcher) snapshot() callbackSnapshot {
	if d == nil {
		return callbackSnapshot{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.stats
}
