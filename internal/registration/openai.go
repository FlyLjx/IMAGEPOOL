package registration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	proxyservice "imagepool/internal/proxy"
)

const (
	defaultAuthURL     = "https://auth.openai.com"
	defaultPlatformURL = "https://platform.openai.com"
	defaultSentinelURL = "https://sentinel.openai.com"
	platformClientID   = "app_2SKx67EdpoN0G6j64rFvigXD"
	platformAudience   = "https://api.openai.com/v1"
	platformAuth0      = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9"
	registrationUA     = accounts.DefaultBrowserUserAgent
)

type WorkerOptions struct {
	AuthURL     string
	PlatformURL string
	SentinelURL string
	HTTPClient  *http.Client
	Mail        MailboxProvider
	Now         func() time.Time
}

func NewWorker(options WorkerOptions) Worker {
	if options.AuthURL == "" {
		options.AuthURL = defaultAuthURL
	}
	if options.PlatformURL == "" {
		options.PlatformURL = defaultPlatformURL
	}
	if options.SentinelURL == "" {
		options.SentinelURL = defaultSentinelURL
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	mailSelector := &atomic.Uint64{}
	return func(ctx context.Context, settings Config, index int) (accounts.Account, error) {
		client, err := registrationHTTPClient(settings, options.HTTPClient)
		if err != nil {
			return accounts.Account{}, err
		}
		mailProvider := options.Mail
		if mailProvider == nil {
			mailProvider = newTempMailProvider(client, mailSelector)
		}
		worker := registrar{client: client, mail: mailProvider, settings: settings, authURL: strings.TrimRight(options.AuthURL, "/"), platformURL: strings.TrimRight(options.PlatformURL, "/"), sentinelURL: strings.TrimRight(options.SentinelURL, "/"), now: options.Now}
		return worker.register(ctx, index)
	}
}

type registrar struct {
	client      *http.Client
	mail        MailboxProvider
	settings    Config
	authURL     string
	platformURL string
	sentinelURL string
	now         func() time.Time
	deviceID    string
	verifier    string
	authCode    string
}

func (r *registrar) register(ctx context.Context, index int) (accounts.Account, error) {
	logStep(ctx, fmt.Sprintf("任务%d：创建临时邮箱", index), "yellow")
	mailbox, err := r.mail.Create(ctx, r.settings.Mail)
	if err != nil {
		return accounts.Account{}, err
	}
	password, err := randomString(20)
	if err != nil {
		return accounts.Account{}, err
	}
	r.deviceID, err = randomUUID()
	if err != nil {
		return accounts.Account{}, err
	}
	if err := r.preloadClearance(ctx); err != nil {
		return accounts.Account{}, err
	}
	logStep(ctx, fmt.Sprintf("任务%d：platform authorize", index), "yellow")
	if err := r.authorize(ctx, mailbox.Address); err != nil {
		return accounts.Account{}, err
	}
	if err := r.registerUser(ctx, mailbox.Address, password); err != nil {
		return accounts.Account{}, err
	}
	if err := r.sendOTP(ctx); err != nil {
		return accounts.Account{}, err
	}
	logStep(ctx, fmt.Sprintf("任务%d：等待邮箱验证码", index), "yellow")
	code, err := r.mail.WaitForCode(ctx, r.settings.Mail, mailbox)
	if err != nil {
		return accounts.Account{}, err
	}
	if err := r.validateOTP(ctx, code); err != nil {
		return accounts.Account{}, err
	}
	if err := r.createAccount(ctx); err != nil {
		return accounts.Account{}, err
	}
	tokens, err := r.exchangeToken(ctx)
	if err != nil {
		return accounts.Account{}, err
	}
	if tokens.AccessToken == "" {
		return accounts.Account{}, fmt.Errorf("oauth token response has no access_token")
	}
	return accounts.Account{Email: mailbox.Address, Password: password, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken, Type: "web", SourceType: "web", Status: "正常", CreatedAt: r.now().Unix(), DeviceID: r.deviceID, UserAgent: registrationUA}, nil
}

func (r *registrar) authorize(ctx context.Context, email string) error {
	verifier, err := randomURLString(64)
	if err != nil {
		return err
	}
	r.verifier = verifier
	challenge := base64.RawURLEncoding.EncodeToString(sha256Bytes(verifier))
	params := url.Values{"issuer": {r.authURL}, "client_id": {platformClientID}, "audience": {platformAudience}, "redirect_uri": {r.platformURL + "/auth/callback"}, "device_id": {r.deviceID}, "screen_hint": {"login_or_signup"}, "max_age": {"0"}, "login_hint": {email}, "scope": {"openid profile email offline_access"}, "response_type": {"code"}, "response_mode": {"query"}, "state": {mustRandomURLString(32)}, "nonce": {mustRandomURLString(32)}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "auth0Client": {platformAuth0}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.authURL+"/api/accounts/authorize?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", registrationUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Referer", r.platformURL+"/")
	return expectStatus(r.client, req, http.StatusOK)
}

func (r *registrar) registerUser(ctx context.Context, email, password string) error {
	token, err := r.sentinel(ctx, "username_password_create")
	if err != nil {
		return err
	}
	return r.postJSON(ctx, "/api/accounts/user/register", map[string]string{"username": email, "password": password}, r.authHeaders(r.authURL+"/create-account/password", token), http.StatusOK, nil)
}

func (r *registrar) sendOTP(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.authURL+"/api/accounts/email-otp/send", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", registrationUA)
	req.Header.Set("Referer", r.authURL+"/create-account/password")
	return expectStatus(r.client, req, http.StatusOK, http.StatusFound)
}

func (r *registrar) validateOTP(ctx context.Context, code string) error {
	headers := r.authHeaders(r.authURL+"/email-verification", "")
	err := r.postJSON(ctx, "/api/accounts/email-otp/validate", map[string]string{"code": code}, headers, http.StatusOK, nil)
	if err == nil {
		return nil
	}
	token, tokenErr := r.sentinel(ctx, "authorize_continue")
	if tokenErr != nil {
		return err
	}
	headers = r.authHeaders(r.authURL+"/email-verification", token)
	return r.postJSON(ctx, "/api/accounts/email-otp/validate", map[string]string{"code": code}, headers, http.StatusOK, nil)
}

func (r *registrar) createAccount(ctx context.Context) error {
	token, err := r.sentinel(ctx, "oauth_create_account")
	if err != nil {
		return err
	}
	name, err := randomString(10)
	if err != nil {
		return err
	}
	var response struct {
		ContinueURL string `json:"continue_url"`
	}
	if err := r.postJSON(ctx, "/api/accounts/create_account", map[string]string{"name": "IMAGE " + name, "birthdate": "1990-01-01"}, r.authHeaders(r.authURL+"/about-you", token), http.StatusOK, &response); err != nil {
		return err
	}
	parsed, err := url.Parse(response.ContinueURL)
	if err != nil || parsed.Query().Get("code") == "" {
		return fmt.Errorf("create account response has no oauth callback code")
	}
	r.authCode = parsed.Query().Get("code")
	return nil
}

func (r *registrar) exchangeToken(ctx context.Context) (oauthTokens, error) {
	if r.authCode == "" {
		return oauthTokens{}, fmt.Errorf("missing oauth callback code")
	}
	var tokens oauthTokens
	body := map[string]string{"client_id": platformClientID, "code_verifier": r.verifier, "grant_type": "authorization_code", "code": r.authCode, "redirect_uri": r.platformURL + "/auth/callback"}
	if err := r.postURL(ctx, r.authURL+"/api/accounts/oauth/token", body, map[string]string{"Accept": "*/*", "Content-Type": "application/json", "Origin": r.platformURL, "Referer": r.platformURL + "/", "Auth0-Client": platformAuth0, "User-Agent": registrationUA}, http.StatusOK, &tokens); err != nil {
		return oauthTokens{}, err
	}
	return tokens, nil
}

type oauthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

func (r *registrar) sentinel(ctx context.Context, flow string) (string, error) {
	p, err := sentinelRequirementsToken(r.deviceID)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"p": p, "id": r.deviceID, "flow": flow})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.sentinelURL+"/backend-api/sentinel/req", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Origin", r.sentinelURL)
	req.Header.Set("Referer", r.sentinelURL+"/backend-api/sentinel/frame.html")
	req.Header.Set("User-Agent", registrationUA)
	var response struct {
		Token       string `json:"token"`
		ProofOfWork struct {
			Required   bool   `json:"required"`
			Seed       string `json:"seed"`
			Difficulty string `json:"difficulty"`
		} `json:"proofofwork"`
	}
	if err := decodeOK(r.client, req, &response); err != nil {
		return "", fmt.Errorf("sentinel: %w", err)
	}
	if response.Token == "" {
		return "", fmt.Errorf("sentinel response has no token")
	}
	proof := p
	if response.ProofOfWork.Required {
		proof, err = sentinelProofToken(r.deviceID, response.ProofOfWork.Seed, response.ProofOfWork.Difficulty)
		if err != nil {
			return "", err
		}
	}
	result, _ := json.Marshal(map[string]string{"p": proof, "t": "", "c": response.Token, "id": r.deviceID, "flow": flow})
	return string(result), nil
}

func (r *registrar) authHeaders(referer, sentinel string) map[string]string {
	headers := map[string]string{"Accept": "application/json", "Content-Type": "application/json", "Origin": r.authURL, "Referer": referer, "User-Agent": registrationUA, "OAI-Device-Id": r.deviceID}
	if sentinel != "" {
		headers["OpenAI-Sentinel-Token"] = sentinel
	}
	return headers
}

func (r *registrar) preloadClearance(ctx context.Context) error {
	settings := r.settings.FlareSolverr
	if !settings.Enabled || strings.TrimSpace(settings.URL) == "" {
		return nil
	}
	if r.client.Jar == nil {
		return fmt.Errorf("registration HTTP client has no cookie jar for flaresolverr clearance")
	}
	timeoutSeconds := settings.MaxTimeoutMS / 1000
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	runtime := proxyRuntime(r.settings.Proxy)
	runtime.Enabled = true
	runtime.Clearance = config.ClearanceRuntime{Enabled: true, Mode: "flaresolverr", FlareSolverrURL: settings.URL, TimeoutSec: timeoutSeconds}
	solution, err := proxyservice.SolveFlareSolverr(ctx, runtime, r.authURL)
	if err != nil {
		return fmt.Errorf("flaresolverr preload: %w", err)
	}
	cookies := parseCookieHeader(solution.Cookies)
	if len(cookies) == 0 && solution.Clearance != "" {
		cookies = []*http.Cookie{{Name: "cf_clearance", Value: solution.Clearance, Path: "/"}}
	}
	if len(cookies) == 0 {
		return fmt.Errorf("flaresolverr preload returned no usable cookies")
	}
	for _, rawURL := range []string{r.authURL, r.platformURL, r.sentinelURL} {
		endpoint, parseErr := url.Parse(rawURL)
		if parseErr == nil {
			r.client.Jar.SetCookies(endpoint, cookies)
		}
	}
	return nil
}

func parseCookieHeader(value string) []*http.Cookie {
	items := strings.Split(value, ";")
	cookies := make([]*http.Cookie, 0, len(items))
	for _, item := range items {
		parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: strings.TrimSpace(parts[0]), Value: strings.TrimSpace(parts[1]), Path: "/"})
	}
	return cookies
}

func (r *registrar) postJSON(ctx context.Context, path string, input any, headers map[string]string, expected int, output any) error {
	return r.postURL(ctx, r.authURL+path, input, headers, expected, output)
}
func (r *registrar) postURL(ctx context.Context, endpoint string, input any, headers map[string]string, expected int, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if output == nil {
		return expectStatus(r.client, req, expected)
	}
	return decodeExpected(r.client, req, expected, output)
}

func registrationHTTPClient(settings Config, base *http.Client) (*http.Client, error) {
	if base != nil {
		cloned := *base
		if cloned.Jar == nil {
			jar, err := cookiejar.New(nil)
			if err != nil {
				return nil, err
			}
			cloned.Jar = jar
		}
		return &cloned, nil
	}
	client, err := proxyservice.NewHTTPClientForRuntime(proxyRuntime(settings.Proxy), time.Duration(settings.Mail.RequestTimeout)*time.Second)
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client.Jar = jar
	return client, nil
}

func proxyRuntime(proxyURL string) config.ProxyRuntime {
	return config.ProxyRuntime{Enabled: strings.TrimSpace(proxyURL) != "", EgressMode: "single_proxy", ProxyURL: proxyURL, SkipSSLVerify: true}
}

func decodeOK(client *http.Client, req *http.Request, output any) error {
	return decodeExpected(client, req, http.StatusOK, output)
}

func decodeExpected(client *http.Client, req *http.Request, expected int, output any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != expected {
		var body bytes.Buffer
		_, _ = body.ReadFrom(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(body.String()))
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
		return fmt.Errorf("decode %s: %w", req.URL.Path, err)
	}
	return nil
}

func expectStatus(client *http.Client, req *http.Request, expected ...int) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for _, status := range expected {
		if resp.StatusCode == status {
			return nil
		}
	}
	var body bytes.Buffer
	_, _ = body.ReadFrom(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("%s %s: HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(body.String()))
}

func randomURLString(length int) (string, error) {
	if length < 1 {
		return "", fmt.Errorf("random length must be positive")
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func mustRandomURLString(length int) string {
	value, err := randomURLString(length)
	if err != nil {
		return "state"
	}
	return value
}

func randomString(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, length)
	for i := range buf {
		value, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		buf[i] = alphabet[value.Int64()]
	}
	return string(buf), nil
}

func randomUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

func sha256Bytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func sentinelRequirementsToken(deviceID string) (string, error) {
	config, err := sentinelPOWConfig(deviceID)
	if err != nil {
		return "", err
	}
	config[3] = 1
	config[9] = 10
	return encodeSentinelConfig("gAAAAAC", config)
}

func sentinelPOWConfig(deviceID string) ([]any, error) {
	sid, err := randomUUID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return []any{"1920x1080", now.Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)"), 4294705152, 0, registrationUA, "https://sentinel.openai.com/sentinel/sdk.js", nil, nil, "en-US", 0, "vendorSub-undefined", "location", "Object", float64(now.UnixMilli()), sid, deviceID, 8, float64(now.UnixMilli())}, nil
}

func encodeSentinelConfig(prefix string, config []any) (string, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return prefix + base64.StdEncoding.EncodeToString(raw), nil
}

func sentinelProofToken(deviceID, seed, difficulty string) (string, error) {
	if strings.TrimSpace(seed) == "" {
		return "", fmt.Errorf("sentinel proof is missing seed")
	}
	difficulty = strings.ToLower(strings.TrimSpace(difficulty))
	if difficulty == "" || len(difficulty) > 8 {
		return "", fmt.Errorf("invalid sentinel proof difficulty %q", difficulty)
	}
	config, err := sentinelPOWConfig(deviceID)
	if err != nil {
		return "", err
	}
	started := time.Now()
	for i := 0; i < 500000; i++ {
		config[3] = i
		config[9] = int(time.Since(started).Milliseconds())
		candidate, err := encodeSentinelConfig("", config)
		if err != nil {
			return "", err
		}
		if sentinelFNV(seed + candidate)[:len(difficulty)] <= difficulty {
			return "gAAAAAB" + candidate + "~S", nil
		}
	}
	return "", fmt.Errorf("unable to satisfy sentinel proof of work")
}

func sentinelFNV(value string) string {
	var h uint32 = 2166136261
	for _, char := range value {
		h ^= uint32(char)
		h *= 16777619
	}
	h ^= h >> 16
	h *= 2246822507
	h ^= h >> 13
	h *= 3266489909
	h ^= h >> 16
	return fmt.Sprintf("%08x", h)
}
