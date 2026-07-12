package oauthlogin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const passwordLoginUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36"

type passwordLoginSession struct {
	client            *http.Client
	deviceID          string
	verifier          string
	authorizeURL      string
	authorizationCode string
}

// ReLogin uses a saved email/password pair to establish a fresh OAuth login
// session. It is intentionally used only by the asynchronous recovery worker,
// never from an image request.
func (s *Service) ReLogin(ctx context.Context, email, password string) (accessToken, refreshToken, idToken string, err error) {
	email = strings.TrimSpace(email)
	password = strings.TrimSpace(password)
	if email == "" || password == "" {
		return "", "", "", fmt.Errorf("missing_credentials: account has no saved email or password")
	}
	session, err := s.beginPasswordLogin(ctx, email)
	if err != nil {
		return "", "", "", err
	}
	code := session.authorizationCode
	if code == "" {
		var emailVerificationRequired bool
		code, emailVerificationRequired, err = s.verifyPassword(ctx, session, password)
		if err != nil {
			return "", "", "", err
		}
		if emailVerificationRequired {
			if err := s.sendEmailOTP(ctx, session); err != nil {
				return "", "", "", err
			}
			reader := s.currentEmailOTPReader()
			if reader == nil {
				return "", "", "", fmt.Errorf("email_verification_required: password login requires an email OTP but no mailbox reader is configured")
			}
			verificationCode, readErr := reader.ReadVerificationCode(ctx, email)
			if readErr != nil {
				return "", "", "", fmt.Errorf("read email verification code: %w", readErr)
			}
			code, err = s.validateEmailOTP(ctx, session, verificationCode)
			if err != nil {
				return "", "", "", err
			}
		}
	}
	if code == "" {
		code, err = s.readAuthorizedCode(ctx, session)
		if err != nil {
			return "", "", "", err
		}
	}
	payload, _ := json.Marshal(map[string]string{
		"client_id":     platformClientID,
		"code_verifier": session.verifier,
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  platformRedirect,
	})
	tokens, err := s.requestTokensWithClient(ctx, session.client, payload, "password login")
	if err != nil {
		return "", "", "", err
	}
	if strings.TrimSpace(tokens.RefreshToken) == "" {
		return "", "", "", fmt.Errorf("password login response did not include refresh_token")
	}
	return tokens.AccessToken, tokens.RefreshToken, tokens.IDToken, nil
}

func (s *Service) beginPasswordLogin(ctx context.Context, email string) (passwordLoginSession, error) {
	client, err := s.newCookieClient()
	if err != nil {
		return passwordLoginSession{}, err
	}
	deviceID, err := passwordLoginDeviceID()
	if err != nil {
		return passwordLoginSession{}, err
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		return passwordLoginSession{}, err
	}
	state, err := randomURLToken(24)
	if err != nil {
		return passwordLoginSession{}, err
	}
	nonce, err := randomURLToken(24)
	if err != nil {
		return passwordLoginSession{}, err
	}
	params := url.Values{
		"issuer":                {s.authBase},
		"client_id":             {platformClientID},
		"audience":              {platformAudience},
		"redirect_uri":          {platformRedirect},
		"device_id":             {deviceID},
		"screen_hint":           {"login"},
		"max_age":               {"0"},
		"login_hint":            {email},
		"scope":                 {"openid profile email offline_access"},
		"response_type":         {"code"},
		"response_mode":         {"query"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge(verifier)},
		"code_challenge_method": {"S256"},
		"auth0Client":           {platformAuth0Agent},
	}
	authorizeURL := s.authBase + "/api/accounts/authorize?" + params.Encode()
	if authURL, parseErr := url.Parse(s.authBase); parseErr == nil && client.Jar != nil {
		client.Jar.SetCookies(authURL, []*http.Cookie{{Name: "oai-did", Value: deviceID, Path: "/"}})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorizeURL, nil)
	if err != nil {
		return passwordLoginSession{}, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Referer", platformBase+"/")
	req.Header.Set("User-Agent", passwordLoginUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return passwordLoginSession{}, fmt.Errorf("password login authorize: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	code := authorizationCodeFrom(raw, finalURL)
	if code == "" && (resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices) {
		return passwordLoginSession{}, passwordLoginHTTPError("authorize", resp.StatusCode, raw)
	}
	return passwordLoginSession{client: client, deviceID: deviceID, verifier: verifier, authorizeURL: authorizeURL, authorizationCode: code}, nil
}

func (s *Service) verifyPassword(ctx context.Context, session passwordLoginSession, password string) (code string, emailVerificationRequired bool, err error) {
	sentinel, err := s.passwordLoginSentinel(ctx, session.client, session.deviceID, "password_verify")
	if err != nil {
		return "", false, fmt.Errorf("password login sentinel: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.authBase+"/api/accounts/password/verify", bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	for key, value := range s.passwordLoginHeaders(s.authBase+"/email-verification", session.deviceID) {
		req.Header.Set(key, value)
	}
	req.Header.Set("OpenAI-Sentinel-Token", sentinel)
	resp, err := session.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("password login verify: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", false, passwordLoginHTTPError("password verify", resp.StatusCode, raw)
	}
	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	code = authorizationCodeFrom(raw, finalURL)
	if code != "" {
		return code, false, nil
	}
	var payload struct {
		Page struct {
			Type string `json:"type"`
		} `json:"page"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &payload)
	pageType := strings.ToLower(strings.TrimSpace(payload.Page.Type))
	if pageType == "" {
		pageType = strings.ToLower(strings.TrimSpace(payload.Type))
	}
	return "", strings.Contains(pageType, "email") && strings.Contains(pageType, "otp"), nil
}

func (s *Service) sendEmailOTP(ctx context.Context, session passwordLoginSession) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.authBase+"/api/accounts/email-otp/send", nil)
	if err != nil {
		return err
	}
	for key, value := range s.passwordLoginHeaders(s.authBase+"/email-verification", session.deviceID) {
		if key == "Content-Type" {
			continue
		}
		req.Header.Set(key, value)
	}
	resp, err := session.client.Do(req)
	if err != nil {
		return fmt.Errorf("send email verification code: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return passwordLoginHTTPError("send email verification code", resp.StatusCode, raw)
	}
	return nil
}

func (s *Service) validateEmailOTP(ctx context.Context, session passwordLoginSession, verificationCode string) (string, error) {
	verificationCode = strings.TrimSpace(verificationCode)
	if verificationCode == "" {
		return "", fmt.Errorf("email verification code is empty")
	}
	code, err := s.postEmailOTPValidation(ctx, session, verificationCode, "")
	if err == nil {
		return code, nil
	}
	firstErr := err
	sentinel, sentinelErr := s.passwordLoginSentinel(ctx, session.client, session.deviceID, "authorize_continue")
	if sentinelErr != nil {
		return "", fmt.Errorf("validate email verification code: %w", firstErr)
	}
	code, err = s.postEmailOTPValidation(ctx, session, verificationCode, sentinel)
	if err != nil {
		return "", fmt.Errorf("validate email verification code: %w", err)
	}
	return code, nil
}

func (s *Service) postEmailOTPValidation(ctx context.Context, session passwordLoginSession, verificationCode, sentinel string) (string, error) {
	body, _ := json.Marshal(map[string]string{"code": verificationCode})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.authBase+"/api/accounts/email-otp/validate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	for key, value := range s.passwordLoginHeaders(s.authBase+"/email-verification", session.deviceID) {
		req.Header.Set(key, value)
	}
	if strings.TrimSpace(sentinel) != "" {
		req.Header.Set("OpenAI-Sentinel-Token", sentinel)
	}
	resp, err := session.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("email verification validation: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", passwordLoginHTTPError("email verification validation", resp.StatusCode, raw)
	}
	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return authorizationCodeFrom(raw, finalURL), nil
}

func (s *Service) readAuthorizedCode(ctx context.Context, session passwordLoginSession) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, session.authorizeURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Referer", platformBase+"/")
	req.Header.Set("User-Agent", passwordLoginUserAgent)
	resp, err := session.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("password login read authorization: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	if code := authorizationCodeFrom(raw, finalURL); code != "" {
		return code, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", passwordLoginHTTPError("read authorization", resp.StatusCode, raw)
	}
	return "", fmt.Errorf("password login authorization did not return an OAuth code")
}

func (s *Service) newCookieClient() (*http.Client, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("oauth service is not configured")
	}
	clone := *s.client
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	clone.Jar = jar
	return &clone, nil
}

func (s *Service) passwordLoginHeaders(referer, deviceID string) map[string]string {
	return map[string]string{
		"Accept":        "application/json",
		"Content-Type":  "application/json",
		"Origin":        s.authBase,
		"Referer":       referer,
		"User-Agent":    passwordLoginUserAgent,
		"OAI-Device-Id": deviceID,
	}
}

func (s *Service) passwordLoginSentinel(ctx context.Context, client *http.Client, deviceID, flow string) (string, error) {
	proof, err := passwordLoginRequirementsToken(deviceID)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"p": proof, "id": deviceID, "flow": flow})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.sentinelBase+"/backend-api/sentinel/req", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Origin", s.sentinelBase)
	req.Header.Set("Referer", s.sentinelBase+"/backend-api/sentinel/frame.html")
	req.Header.Set("User-Agent", passwordLoginUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", passwordLoginHTTPError("sentinel", resp.StatusCode, raw)
	}
	var result struct {
		Token       string `json:"token"`
		ProofOfWork struct {
			Required   bool   `json:"required"`
			Seed       string `json:"seed"`
			Difficulty string `json:"difficulty"`
		} `json:"proofofwork"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("decode sentinel response: %w", err)
	}
	if strings.TrimSpace(result.Token) == "" {
		return "", fmt.Errorf("sentinel response has no token")
	}
	if result.ProofOfWork.Required {
		proof, err = passwordLoginProofToken(ctx, deviceID, result.ProofOfWork.Seed, result.ProofOfWork.Difficulty)
		if err != nil {
			return "", err
		}
	}
	encoded, _ := json.Marshal(map[string]string{"p": proof, "t": "", "c": result.Token, "id": deviceID, "flow": flow})
	return string(encoded), nil
}

func (s *Service) requestTokensWithClient(ctx context.Context, client *http.Client, payload []byte, operation string) (Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.authBase+"/api/accounts/oauth/token", bytes.NewReader(payload))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", platformBase)
	req.Header.Set("Referer", platformBase+"/")
	req.Header.Set("Auth0-Client", platformAuth0Agent)
	req.Header.Set("User-Agent", passwordLoginUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("exchange oauth code: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tokens Tokens
	_ = json.Unmarshal(raw, &tokens)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || strings.TrimSpace(tokens.AccessToken) == "" {
		return Tokens{}, passwordLoginHTTPError("oauth token "+operation, resp.StatusCode, raw)
	}
	return tokens, nil
}

func authorizationCodeFrom(raw []byte, callback string) string {
	if code := callbackCode(callback); code != "" {
		return code
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	for _, key := range []string{"continue_url", "redirect_url", "redirectUrl", "url"} {
		if code := callbackCode(strings.TrimSpace(fmt.Sprint(payload[key]))); code != "" {
			return code
		}
	}
	for _, key := range []string{"authorization_code", "authorizationCode", "code"} {
		if code := strings.TrimSpace(fmt.Sprint(payload[key])); code != "" && code != "<nil>" {
			return code
		}
	}
	return ""
}

func callbackCode(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("code"))
}

func passwordLoginHTTPError(operation string, status int, raw []byte) error {
	message := strings.TrimSpace(string(raw))
	if len(message) > 500 {
		message = message[:500]
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("%s rejected (HTTP %d): %s", operation, status, message)
}

func passwordLoginDeviceID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

func passwordLoginRequirementsToken(deviceID string) (string, error) {
	config, err := passwordLoginPOWConfig(deviceID)
	if err != nil {
		return "", err
	}
	config[3] = 1
	config[9] = 10
	return "gAAAAAC" + base64.StdEncoding.EncodeToString(mustJSON(config)), nil
}

func passwordLoginProofToken(ctx context.Context, deviceID, seed, difficulty string) (string, error) {
	seed = strings.TrimSpace(seed)
	difficulty = strings.ToLower(strings.TrimSpace(difficulty))
	if seed == "" || difficulty == "" || len(difficulty) > 8 {
		return "", fmt.Errorf("invalid sentinel proof parameters")
	}
	config, err := passwordLoginPOWConfig(deviceID)
	if err != nil {
		return "", err
	}
	started := time.Now()
	for index := 0; index < 500000; index++ {
		if index%1024 == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		config[3] = index
		config[9] = int(time.Since(started).Milliseconds())
		candidate := base64.StdEncoding.EncodeToString(mustJSON(config))
		if passwordLoginFNV(seed + candidate)[:len(difficulty)] <= difficulty {
			return "gAAAAAB" + candidate + "~S", nil
		}
	}
	return "", fmt.Errorf("unable to satisfy sentinel proof of work")
}

func passwordLoginPOWConfig(deviceID string) ([]any, error) {
	sessionID, err := passwordLoginDeviceID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return []any{"1920x1080", now.Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)"), 4294705152, 0, passwordLoginUserAgent, "https://sentinel.openai.com/sentinel/sdk.js", nil, nil, "en-US", 0, "vendorSub-undefined", "location", "Object", float64(now.UnixMilli()), sessionID, deviceID, 8, float64(now.UnixMilli())}, nil
}

func passwordLoginFNV(value string) string {
	var hash uint32 = 2166136261
	for _, char := range value {
		hash ^= uint32(char)
		hash *= 16777619
	}
	hash ^= hash >> 16
	hash *= 2246822507
	hash ^= hash >> 13
	hash *= 3266489909
	hash ^= hash >> 16
	return fmt.Sprintf("%08x", hash)
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
