package oauthlogin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultAuthBase     = "https://auth.openai.com"
	defaultSentinelBase = "https://sentinel.openai.com"
	platformBase        = "https://platform.openai.com"
	platformClientID    = "app_2SKx67EdpoN0G6j64rFvigXD"
	platformAudience    = "https://api.openai.com/v1"
	platformRedirect    = platformBase + "/auth/callback"
	platformAuth0Agent  = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9"
	sessionTTL          = 10 * time.Minute
	maxSessions         = 64
)

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

type session struct {
	Verifier string
	State    string
	Created  time.Time
}

// Service manages short-lived PKCE sessions for the manual OAuth bridge.
type Service struct {
	mu             sync.Mutex
	sessions       map[string]session
	authBase       string
	sentinelBase   string
	client         *http.Client
	emailOTPReader EmailOTPReader
	now            func() time.Time
}

func New() *Service { return NewWithClientAndSentinel(defaultAuthBase, defaultSentinelBase, nil) }

func NewWithClient(authBase string, client *http.Client) *Service {
	return NewWithClientAndSentinel(authBase, defaultSentinelBase, client)
}

func NewWithClientAndSentinel(authBase, sentinelBase string, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	authBase = strings.TrimRight(strings.TrimSpace(authBase), "/")
	if authBase == "" {
		authBase = defaultAuthBase
	}
	sentinelBase = strings.TrimRight(strings.TrimSpace(sentinelBase), "/")
	if sentinelBase == "" {
		sentinelBase = defaultSentinelBase
	}
	return &Service{sessions: map[string]session{}, authBase: authBase, sentinelBase: sentinelBase, client: client, now: time.Now}
}

// SetEmailOTPReader configures automatic mailbox polling for background
// password recovery. Passing nil disables OTP-based re-login.
func (s *Service) SetEmailOTPReader(reader EmailOTPReader) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.emailOTPReader = reader
	s.mu.Unlock()
}

func (s *Service) currentEmailOTPReader() EmailOTPReader {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emailOTPReader
}

func (s *Service) Start(emailHint string) (map[string]string, error) {
	if s == nil {
		return nil, fmt.Errorf("oauth service is not configured")
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		return nil, err
	}
	sessionID, err := randomURLToken(18)
	if err != nil {
		return nil, err
	}
	nonce, err := randomURLToken(24)
	if err != nil {
		return nil, err
	}
	stateNonce, err := randomURLToken(16)
	if err != nil {
		return nil, err
	}
	state := sessionID + "." + stateNonce
	params := url.Values{
		"issuer":                {s.authBase},
		"client_id":             {platformClientID},
		"audience":              {platformAudience},
		"redirect_uri":          {platformRedirect},
		"device_id":             {nonce},
		"screen_hint":           {"login_or_signup"},
		"max_age":               {"0"},
		"scope":                 {"openid profile email offline_access"},
		"response_type":         {"code"},
		"response_mode":         {"query"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge(verifier)},
		"code_challenge_method": {"S256"},
		"auth0Client":           {platformAuth0Agent},
	}
	if hint := strings.TrimSpace(emailHint); hint != "" {
		params.Set("login_hint", hint)
	}
	s.mu.Lock()
	s.purgeLocked()
	s.sessions[sessionID] = session{Verifier: verifier, State: state, Created: s.now()}
	s.trimLocked()
	s.mu.Unlock()
	return map[string]string{
		"session_id":          sessionID,
		"authorize_url":       s.authBase + "/api/accounts/authorize?" + params.Encode(),
		"expires_in":          fmt.Sprintf("%d", int(sessionTTL.Seconds())),
		"redirect_uri_prefix": platformRedirect,
	}, nil
}

func (s *Service) Finish(sessionID, callback string) (Tokens, error) {
	code, state := callbackParts(callback)
	if code == "" {
		return Tokens{}, fmt.Errorf("missing code or callback URL")
	}
	if stateID, _, found := strings.Cut(state, "."); found && stateID != "" {
		sessionID = stateID
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Tokens{}, fmt.Errorf("missing oauth session_id")
	}
	s.mu.Lock()
	s.purgeLocked()
	entry, ok := s.sessions[sessionID]
	s.mu.Unlock()
	if !ok {
		return Tokens{}, fmt.Errorf("oauth session expired or not found; start a new authorization")
	}
	if state != "" && state != entry.State {
		return Tokens{}, fmt.Errorf("oauth state does not match the active session")
	}
	tokens, err := s.exchange(code, entry.Verifier)
	if err != nil {
		return Tokens{}, err
	}
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	return tokens, nil
}

func (s *Service) exchange(code, verifier string) (Tokens, error) {
	payload, _ := json.Marshal(map[string]string{
		"client_id":     platformClientID,
		"code_verifier": verifier,
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  platformRedirect,
	})
	tokens, err := s.requestTokens(context.Background(), payload, "exchange")
	if err != nil {
		return Tokens{}, err
	}
	if strings.TrimSpace(tokens.RefreshToken) == "" {
		return Tokens{}, fmt.Errorf("oauth response did not include refresh_token")
	}
	return tokens, nil
}

// RefreshToken exchanges a saved OAuth refresh token outside the request path.
// The caller keeps the previous refresh/id token when OpenAI does not rotate
// them in the response.
func (s *Service) RefreshToken(ctx context.Context, refreshToken string) (accessToken, nextRefreshToken, idToken string, err error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return "", "", "", fmt.Errorf("refresh_token is required")
	}
	payload, _ := json.Marshal(map[string]string{
		"client_id":     platformClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	tokens, err := s.requestTokens(ctx, payload, "refresh")
	if err != nil {
		return "", "", "", err
	}
	return tokens.AccessToken, tokens.RefreshToken, tokens.IDToken, nil
}

func (s *Service) requestTokens(ctx context.Context, payload []byte, operation string) (Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.authBase+"/api/accounts/oauth/token", bytes.NewReader(payload))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", platformBase)
	req.Header.Set("Referer", platformBase+"/")
	req.Header.Set("Auth0-Client", platformAuth0Agent)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("exchange oauth code: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tokens Tokens
	_ = json.Unmarshal(raw, &tokens)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || strings.TrimSpace(tokens.AccessToken) == "" {
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = resp.Status
		}
		if len(message) > 500 {
			message = message[:500]
		}
		return Tokens{}, fmt.Errorf("oauth token %s rejected (HTTP %d): %s", operation, resp.StatusCode, message)
	}
	return tokens, nil
}

func (s *Service) purgeLocked() {
	deadline := s.now().Add(-sessionTTL)
	for id, entry := range s.sessions {
		if entry.Created.Before(deadline) {
			delete(s.sessions, id)
		}
	}
}

func (s *Service) trimLocked() {
	for len(s.sessions) > maxSessions {
		var oldestID string
		var oldest time.Time
		for id, entry := range s.sessions {
			if oldestID == "" || entry.Created.Before(oldest) {
				oldestID, oldest = id, entry.Created
			}
		}
		delete(s.sessions, oldestID)
	}
}

func callbackParts(value string) (code, state string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.IsAbs() {
		return strings.TrimSpace(parsed.Query().Get("code")), strings.TrimSpace(parsed.Query().Get("state"))
	}
	return value, ""
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomURLToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
