package accounts

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"imagepool/internal/config"
)

// OAuthTokenRefresher exchanges a stored refresh token for a new OAuth token
// set. It intentionally uses primitive return values so the account package
// remains independent from a particular OAuth client implementation.
type OAuthTokenRefresher interface {
	RefreshToken(ctx context.Context, refreshToken string) (accessToken, nextRefreshToken, idToken string, err error)
}

// OAuthPasswordRelogger establishes a fresh OAuth session from account
// credentials. It is only used by the background recovery worker after OpenAI
// explicitly rejects the saved refresh token.
type OAuthPasswordRelogger interface {
	ReLogin(ctx context.Context, email, password string) (accessToken, refreshToken, idToken string, err error)
}

// TokenRecoveryManager restores accounts marked as credential-invalid without
// blocking image, text, or search requests. Accounts only return to the pool
// after their refreshed token has passed AccountChecker validation.
type TokenRecoveryManager struct {
	store     *Store
	checker   AccountChecker
	refresher OAuthTokenRefresher
	relogger  OAuthPasswordRelogger

	mu    sync.RWMutex
	cfg   config.Config
	now   func() time.Time
	wake  chan struct{}
	runMu sync.Mutex
}

func NewTokenRecoveryManager(cfg config.Config, store *Store, checker AccountChecker, refresher OAuthTokenRefresher, reloggers ...OAuthPasswordRelogger) *TokenRecoveryManager {
	var relogger OAuthPasswordRelogger
	if len(reloggers) > 0 {
		relogger = reloggers[0]
	}
	return &TokenRecoveryManager{
		store:     store,
		checker:   checker,
		refresher: refresher,
		relogger:  relogger,
		cfg:       cfg.Normalize(),
		now:       time.Now,
		wake:      make(chan struct{}, 1),
	}
}

func (m *TokenRecoveryManager) UpdateConfig(cfg config.Config) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.cfg = cfg.Normalize()
	m.mu.Unlock()
	m.signalWake()
}

func (m *TokenRecoveryManager) UpdateRefresher(refresher OAuthTokenRefresher) {
	if m == nil || refresher == nil {
		return
	}
	m.mu.Lock()
	m.refresher = refresher
	m.mu.Unlock()
	m.signalWake()
}

func (m *TokenRecoveryManager) UpdatePasswordRelogger(relogger OAuthPasswordRelogger) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.relogger = relogger
	m.mu.Unlock()
	m.signalWake()
}

func (m *TokenRecoveryManager) current() (config.Config, OAuthTokenRefresher, OAuthPasswordRelogger) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg, m.refresher, m.relogger
}

func (m *TokenRecoveryManager) signalWake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run keeps the recovery loop outside request handling. It runs once on
// startup so persisted failed accounts are eligible immediately.
func (m *TokenRecoveryManager) Run(ctx context.Context) {
	if m == nil {
		return
	}
	for {
		m.RecoverDue(ctx)
		cfg, _, _ := m.current()
		wait := time.Duration(cfg.TokenRecoveryIntervalSecs) * time.Second
		if wait <= 0 {
			wait = time.Minute
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		case <-m.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
	}
}

// RecoverDue processes all due recovery candidates and returns after every
// attempt completes. It is exported to make the recovery lifecycle testable.
func (m *TokenRecoveryManager) RecoverDue(ctx context.Context) {
	if m == nil || m.store == nil || m.checker == nil {
		return
	}
	m.runMu.Lock()
	defer m.runMu.Unlock()

	cfg, _, _ := m.current()
	candidates := m.store.PendingTokenRecoveries(m.now())
	if len(candidates) == 0 {
		return
	}
	concurrency := cfg.TokenRecoveryConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(candidates) {
		concurrency = len(candidates)
	}
	jobs := make(chan Account)
	var workers sync.WaitGroup
	for index := 0; index < concurrency; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for account := range jobs {
				m.recoverOne(ctx, account.AccessToken)
			}
		}()
	}
	for _, account := range candidates {
		select {
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		case jobs <- account:
		}
	}
	close(jobs)
	workers.Wait()
}

func (m *TokenRecoveryManager) recoverOne(ctx context.Context, token string) {
	now := m.now()
	account, found, err := m.store.BeginTokenRecovery(token, now)
	if err != nil || !found {
		return
	}
	cfg, refresher, relogger := m.current()
	if refresher == nil {
		m.fail(account.AccessToken, "OAuth token recovery is not configured", cfg)
		return
	}
	if strings.TrimSpace(account.RefreshToken) == "" {
		m.fail(account.AccessToken, "account has no refresh_token for OAuth recovery", cfg)
		return
	}

	timeout := time.Duration(cfg.TokenRecoveryTimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = time.Minute
	}
	recoveryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	accessToken, refreshToken, idToken, err := refresher.RefreshToken(recoveryCtx, account.RefreshToken)
	if err != nil {
		if isRefreshTokenInvalidated(err) {
			m.recoverWithPasswordLogin(recoveryCtx, account, relogger, compactTokenRecoveryError(err), cfg)
			return
		}
		m.fail(account.AccessToken, compactTokenRecoveryError(err), cfg)
		return
	}
	updated, found, err := m.store.ReplaceOAuthTokens(account.AccessToken, accessToken, refreshToken, idToken)
	if err != nil || !found {
		reason := "account disappeared while saving refreshed token"
		if err != nil {
			reason = compactTokenRecoveryError(err)
		}
		targetToken := account.AccessToken
		if found && strings.TrimSpace(updated.AccessToken) != "" {
			targetToken = updated.AccessToken
		}
		m.fail(targetToken, reason, cfg)
		return
	}
	check, err := m.checker.CheckAccount(recoveryCtx, updated.AccessToken)
	if err != nil {
		m.fail(updated.AccessToken, compactTokenRecoveryError(err), cfg)
		return
	}
	if _, found, err := m.store.CompleteTokenRecovery(updated.AccessToken, check); err != nil || !found {
		reason := "account disappeared while completing token recovery"
		if err != nil {
			reason = compactTokenRecoveryError(err)
		}
		m.fail(updated.AccessToken, reason, cfg)
	}
}

func (m *TokenRecoveryManager) recoverWithPasswordLogin(ctx context.Context, account Account, relogger OAuthPasswordRelogger, refreshError string, cfg config.Config) {
	_, _ = m.store.LogTokenRecoveryEvent(
		account.AccessToken,
		"warning",
		"refresh_token_invalidated",
		"OAuth refresh_token 已失效，改用密码和邮箱验证码重新登录",
		refreshError,
	)
	if relogger == nil {
		reason := "refresh_token_invalidated: password re-login is not configured"
		_, _ = m.store.LogTokenRecoveryEvent(account.AccessToken, "error", "password_relogin_failed", "密码重新登录未配置，等待下一次后台恢复", reason)
		m.fail(account.AccessToken, reason, cfg)
		return
	}
	if strings.TrimSpace(account.Email) == "" || strings.TrimSpace(account.Password) == "" {
		reason := "refresh_token_invalidated: account has no saved email or password for re-login"
		_, _ = m.store.LogTokenRecoveryEvent(account.AccessToken, "error", "password_relogin_failed", "账号没有保存邮箱或密码，无法重新登录", reason)
		m.fail(account.AccessToken, reason, cfg)
		return
	}
	_, _ = m.store.LogTokenRecoveryEvent(
		account.AccessToken,
		"processing",
		"password_relogin_started",
		"开始密码重新登录，必要时将读取邮箱验证码",
		"",
	)
	accessToken, refreshToken, idToken, err := relogger.ReLogin(ctx, account.Email, account.Password)
	if err != nil {
		failure := compactTokenRecoveryError(err)
		_, _ = m.store.LogTokenRecoveryEvent(
			account.AccessToken,
			"error",
			"password_relogin_failed",
			"密码重新登录失败，等待下一次后台恢复",
			failure,
		)
		m.fail(account.AccessToken, failure, cfg)
		return
	}
	updated, found, err := m.store.ReplaceOAuthTokensAfterPasswordLogin(account.AccessToken, accessToken, refreshToken, idToken)
	if err != nil || !found {
		reason := "account disappeared while saving password re-login token"
		if err != nil {
			reason = compactTokenRecoveryError(err)
		}
		targetToken := account.AccessToken
		if found && strings.TrimSpace(updated.AccessToken) != "" {
			targetToken = updated.AccessToken
		}
		m.fail(targetToken, reason, cfg)
		return
	}
	check, err := m.checker.CheckAccount(ctx, updated.AccessToken)
	if err != nil {
		m.fail(updated.AccessToken, compactTokenRecoveryError(err), cfg)
		return
	}
	if _, found, err := m.store.CompleteTokenRecovery(updated.AccessToken, check); err != nil || !found {
		reason := "account disappeared while completing password re-login recovery"
		if err != nil {
			reason = compactTokenRecoveryError(err)
		}
		m.fail(updated.AccessToken, reason, cfg)
	}
}

func (m *TokenRecoveryManager) fail(token, reason string, cfg config.Config) {
	_, _ = m.store.FailTokenRecovery(
		token,
		reason,
		cfg.TokenRecoveryMaxAttempts,
		time.Duration(cfg.TokenRecoveryIntervalSecs)*time.Second,
	)
}

func compactTokenRecoveryError(err error) string {
	if err == nil {
		return "OAuth token recovery failed"
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "OAuth token recovery failed"
	}
	if len(message) > 500 {
		return fmt.Sprintf("%s...", message[:500])
	}
	return message
}

func isRefreshTokenInvalidated(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "refresh_token_invalidated")
}
