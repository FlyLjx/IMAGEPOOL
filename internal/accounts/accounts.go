package accounts

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagepool/internal/persistence"
)

var (
	ErrNoAvailableAccount = errors.New("no available account")
	ErrAccountNotFound    = errors.New("account not found")
)

const (
	StatusCredentialInvalid  = "失效"
	StatusCredentialRecovery = "恢复中"

	tokenRecoveryStateKey     = "token_recovery_state"
	tokenRecoveryPending      = "pending"
	tokenRecoveryRunning      = "running"
	tokenRecoveryAttemptsKey  = "token_recovery_attempts"
	tokenRecoveryNextAtKey    = "token_recovery_next_at"
	maxCredentialRecoveryLogs = 500
	accountPersistDebounce    = 100 * time.Millisecond
)

// ImageCooldownReason identifies a temporary dispatch backoff caused by an
// upstream image-generation failure. The account remains usable once the
// cooldown expires.
type ImageCooldownReason string

const (
	ImageCooldownRateLimited          ImageCooldownReason = "rate_limited"
	ImageCooldownUpstreamFailure      ImageCooldownReason = "upstream_failure"
	ImageCooldownGenerationTerminated ImageCooldownReason = "generation_terminated"

	imageCooldownUntilKey     = "image_cooldown_until"
	imageCooldownReasonKey    = "image_cooldown_reason"
	imageCooldownFailuresKey  = "image_cooldown_failures"
	imageCooldownLastErrorKey = "image_cooldown_last_error"
	imageCooldownLastAtKey    = "image_cooldown_last_at"
)

const (
	DefaultBrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36"
	DefaultBrowserSecCHUA   = `"Not_A Brand";v="8", "Chromium";v="144", "Google Chrome";v="144"`
)

// Account keeps the fields used by IMAGE POOL while preserving unrecognized
// fields from the Python service's account JSON for lossless migrations.
type Account struct {
	ID                string            `json:"-"`
	Email             string            `json:"-"`
	AccessToken       string            `json:"-"`
	RefreshToken      string            `json:"-"`
	IDToken           string            `json:"-"`
	Password          string            `json:"-"`
	Type              string            `json:"-"`
	SourceType        string            `json:"-"`
	Status            string            `json:"-"`
	Disabled          bool              `json:"-"`
	Quota             int               `json:"-"`
	ImageQuotaUnknown bool              `json:"-"`
	CreatedAt         int64             `json:"-"`
	ImportedAt        int64             `json:"-"`
	LastUsedAt        int64             `json:"-"`
	LastError         string            `json:"-"`
	ImageOK           int               `json:"-"`
	ImageFailures     int               `json:"-"`
	Proxy             string            `json:"-"`
	FP                map[string]string `json:"-"`
	UserAgent         string            `json:"-"`
	DeviceID          string            `json:"-"`
	SessionID         string            `json:"-"`
	Extra             map[string]any    `json:"-"`
	loadedOrder       int
}

// CredentialRecoveryLog records the background lifecycle for an account
// credential after an upstream authentication failure. It intentionally never
// contains access, refresh, or ID token values.
type CredentialRecoveryLog struct {
	ID           string `json:"id"`
	Time         string `json:"time"`
	Level        string `json:"level"`
	Event        string `json:"event"`
	AccountEmail string `json:"account_email,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
	Message      string `json:"message"`
	Error        string `json:"error,omitempty"`
}

func (a *Account) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var extra map[string]any
	if err := json.Unmarshal(data, &extra); err != nil {
		return err
	}
	*a = Account{Extra: extra}
	a.ID = rawString(raw, "id", "user_id")
	a.Email = rawString(raw, "email")
	a.AccessToken = rawString(raw, "access_token", "accessToken")
	a.RefreshToken = rawString(raw, "refresh_token", "refreshToken")
	a.IDToken = rawString(raw, "id_token", "idToken")
	a.Password = rawString(raw, "password")
	a.Type = rawString(raw, "type")
	a.SourceType = rawString(raw, "source_type")
	a.Status = rawString(raw, "status")
	a.Disabled = rawBool(raw, "disabled")
	a.Quota = rawInt(raw, "quota")
	a.ImageQuotaUnknown = rawBool(raw, "image_quota_unknown")
	a.CreatedAt = rawUnix(raw, "created_at")
	a.ImportedAt = rawUnix(raw, "imported_at")
	a.LastUsedAt = rawUnix(raw, "last_used_at")
	a.LastError = rawString(raw, "last_error", "last_refresh_error")
	a.ImageOK = rawInt(raw, "success", "image_ok")
	a.ImageFailures = rawInt(raw, "fail", "image_failures")
	a.Proxy = rawString(raw, "proxy")
	a.FP = rawStringMap(raw, "fp")
	a.UserAgent = rawString(raw, "user-agent", "user_agent")
	a.DeviceID = rawString(raw, "oai-device-id", "oai_device_id")
	a.SessionID = rawString(raw, "oai-session-id", "oai_session_id")
	return nil
}

func (a Account) MarshalJSON() ([]byte, error) {
	out := cloneMap(a.Extra)
	setString(out, "id", a.ID)
	setString(out, "email", a.Email)
	setString(out, "access_token", a.AccessToken)
	delete(out, "accessToken")
	setString(out, "refresh_token", a.RefreshToken)
	delete(out, "refreshToken")
	setString(out, "id_token", a.IDToken)
	delete(out, "idToken")
	setString(out, "password", a.Password)
	setString(out, "type", a.Type)
	setString(out, "source_type", a.SourceType)
	setString(out, "status", a.Status)
	if a.Disabled {
		out["disabled"] = true
	}
	if a.Quota != 0 || hasKey(out, "quota") {
		out["quota"] = a.Quota
	}
	if a.ImageQuotaUnknown {
		out["image_quota_unknown"] = true
	} else {
		delete(out, "image_quota_unknown")
	}
	if a.CreatedAt > 0 {
		out["created_at"] = timestampValue(out["created_at"], a.CreatedAt)
	}
	if a.ImportedAt > 0 {
		out["imported_at"] = timestampValue(out["imported_at"], a.ImportedAt)
	}
	if a.LastUsedAt > 0 {
		out["last_used_at"] = timestampValue(out["last_used_at"], a.LastUsedAt)
	}
	setString(out, "last_error", a.LastError)
	if a.ImageOK != 0 || hasKey(out, "success") || hasKey(out, "image_ok") {
		out["success"] = a.ImageOK
		delete(out, "image_ok")
	}
	if a.ImageFailures != 0 || hasKey(out, "fail") || hasKey(out, "image_failures") {
		out["fail"] = a.ImageFailures
		delete(out, "image_failures")
	}
	setString(out, "proxy", a.Proxy)
	if len(a.FP) > 0 {
		out["fp"] = a.FP
	}
	setString(out, "user-agent", a.UserAgent)
	setString(out, "oai-device-id", a.DeviceID)
	setString(out, "oai-session-id", a.SessionID)
	return json.Marshal(out)
}

func (a Account) Public() map[string]any {
	data, _ := json.Marshal(a)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	hasPassword := strings.TrimSpace(a.Password) != ""
	for _, key := range []string{"password", "refresh_token", "refreshToken", "id_token", "idToken", "session_token", "cookie"} {
		delete(out, key)
	}
	out["has_password"] = hasPassword
	score, label, reasons := accountHealth(a)
	out["dispatch_score"] = score
	out["health_score"] = score
	out["health_label"] = label
	out["health_reasons"] = reasons
	return out
}

func accountHealth(account Account) (float64, string, []string) {
	score := 100.0
	total := account.ImageOK + account.ImageFailures
	if total > 0 {
		score += 80.0 * float64(account.ImageOK) / float64(total)
		score -= 45.0 * float64(account.ImageFailures) / float64(total)
	} else {
		score += 20.0
	}
	consecutiveFailures := asInt(account.Extra["consecutive_failures"])
	score -= minFloat(80, float64(max(0, consecutiveFailures))*18)
	if account.ImageQuotaUnknown {
		score += 10
	} else {
		score += minFloat(35, float64(max(0, account.Quota))*2)
	}
	if isStatus(account.Status, "正常") {
		score += 8
	}
	if isStatus(account.Status, "限流") {
		score -= 55
	}
	if isStatus(account.Status, StatusCredentialInvalid) {
		score = 0
	} else if isStatus(account.Status, StatusCredentialRecovery) {
		score = minFloat(score, 25)
	} else if isStatus(account.Status, "异常") {
		score -= 85
	}
	if account.Disabled || isStatus(account.Status, "禁用") {
		score = 0
	}
	score = minFloat(100, maxFloat(0, score))
	label := "风险"
	switch {
	case score >= 80:
		label = "优秀"
	case score >= 60:
		label = "良好"
	case score >= 40:
		label = "观察"
	}
	reasons := make([]string, 0, 3)
	if !isStatus(account.Status, "正常") {
		reasons = append(reasons, account.Status)
	}
	if account.ImageQuotaUnknown {
		reasons = append(reasons, "图片额度未知")
	}
	if consecutiveFailures > 0 {
		reasons = append(reasons, fmt.Sprintf("连续失败 %d", consecutiveFailures))
	}
	return score, label, reasons
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

type Store struct {
	mu                         sync.RWMutex
	persist                    sync.Mutex
	path                       string
	state                      persistence.Store
	accounts                   []Account
	credentialRecoveryLogs     []CredentialRecoveryLog
	credentialRecoverySequence uint64
	imageLeases                map[string]struct{}
	imageLeaseChanged          chan struct{}
	imageWaiters               []*imageWaiter
	now                        func() time.Time
	dirty                      bool
	revision                   uint64
	wake                       chan struct{}
	stop                       chan struct{}
	done                       chan struct{}
	close                      sync.Once
}

type imageWaiter struct {
	ready    chan struct{}
	notified bool
}

type fileShape struct {
	Accounts               []Account               `json:"accounts"`
	CredentialRecoveryLogs []CredentialRecoveryLog `json:"credential_recovery_logs,omitempty"`
}

func NewStore(items []Account, path string) *Store {
	return newStore(items, nil, path, nil)
}

func newStore(items []Account, recoveryLogs []CredentialRecoveryLog, path string, state persistence.Store) *Store {
	copied := make([]Account, len(items))
	for i := range copied {
		copied[i] = cloneAccount(items[i])
		copied[i].loadedOrder = i
		if copied[i].Extra == nil {
			copied[i].Extra = map[string]any{}
		}
	}
	logs := append([]CredentialRecoveryLog(nil), recoveryLogs...)
	if len(logs) > maxCredentialRecoveryLogs {
		logs = append([]CredentialRecoveryLog(nil), logs[len(logs)-maxCredentialRecoveryLogs:]...)
	}
	s := &Store{
		path:                   strings.TrimSpace(path),
		state:                  state,
		accounts:               copied,
		credentialRecoveryLogs: logs,
		imageLeases:            map[string]struct{}{},
		imageLeaseChanged:      make(chan struct{}),
		now:                    time.Now,
	}
	if s.path != "" || s.state != nil {
		s.wake = make(chan struct{}, 1)
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		go s.persistenceLoop()
	}
	return s
}

func LoadStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return NewStore(nil, ""), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewStore(nil, path), nil
		}
		return nil, err
	}
	shaped, err := parseStoreShape(data)
	if err != nil {
		return nil, fmt.Errorf("parse accounts: %w", err)
	}
	return newStore(shaped.Accounts, shaped.CredentialRecoveryLogs, path, nil), nil
}

func LoadStoreFromPersistence(state persistence.Store) (*Store, error) {
	var shaped fileShape
	err := state.Load(context.Background(), "accounts", &shaped)
	if err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			return newStore(nil, nil, "", state), nil
		}
		return nil, fmt.Errorf("load accounts from PostgreSQL: %w", err)
	}
	return newStore(shaped.Accounts, shaped.CredentialRecoveryLogs, "", state), nil
}

func NewStoreWithPersistence(items []Account, state persistence.Store) *Store {
	return newStore(items, nil, "", state)
}

func parseStoreShape(data []byte) (fileShape, error) {
	var list []Account
	if err := json.Unmarshal(data, &list); err == nil {
		return fileShape{Accounts: list}, nil
	}
	var shaped fileShape
	if err := json.Unmarshal(data, &shaped); err != nil {
		return fileShape{}, err
	}
	return shaped, nil
}

func (s *Store) List() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, len(s.accounts))
	for index := range s.accounts {
		out[index] = cloneAccount(s.accounts[index])
	}
	return out
}

func (s *Store) PublicList() []map[string]any {
	list := s.List()
	out := make([]map[string]any, 0, len(list))
	for _, account := range list {
		out = append(out, account.Public())
	}
	return out
}

// CredentialRecoveryLogs returns the most recent credential recovery events
// first. Account email filtering keeps the API independent of token values.
func (s *Store) CredentialRecoveryLogs(email string, limit int) []CredentialRecoveryLog {
	email = strings.TrimSpace(email)
	if limit <= 0 || limit > maxCredentialRecoveryLogs {
		limit = maxCredentialRecoveryLogs
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]CredentialRecoveryLog, 0, min(limit, len(s.credentialRecoveryLogs)))
	for index := len(s.credentialRecoveryLogs) - 1; index >= 0 && len(items) < limit; index-- {
		item := s.credentialRecoveryLogs[index]
		if email != "" && !strings.EqualFold(strings.TrimSpace(item.AccountEmail), email) {
			continue
		}
		items = append(items, item)
	}
	return items
}

func (s *Store) Summary() map[string]any {
	list := s.List()
	active, limited, abnormal, disabled, totalQuota := 0, 0, 0, 0, 0
	byType := map[string]int{}
	for _, a := range list {
		totalQuota += a.Quota
		kind := strings.TrimSpace(a.Type)
		if kind == "" {
			kind = "unknown"
		}
		byType[kind]++
		if a.Disabled || isStatus(a.Status, "disabled", "禁用") {
			disabled++
			continue
		}
		if isStatus(a.Status, "limited", "rate_limited", "限流") {
			limited++
			continue
		}
		if isStatus(a.Status, "invalid", "abnormal", "异常", "token_revoked", "token_invalidated", StatusCredentialInvalid, StatusCredentialRecovery) {
			abnormal++
			continue
		}
		if usable(a) {
			active++
		}
	}
	return map[string]any{
		"total": len(list), "cumulative_total": len(list), "active": active, "limited": limited,
		"abnormal": abnormal, "disabled": disabled, "cooling": 0, "total_quota": totalQuota,
		"unlimited_quota_count": 0, "total_success": sumImageOK(list), "total_fail": sumImageFailures(list),
		"by_type": byType, "by_error_type": map[string]int{}, "proxy_stats": map[string]any{"accounts": 0, "success": 0, "fail": 0, "cooling": 0, "by_error_type": map[string]int{}},
	}
}

func (s *Store) Add(items []Account) error {
	_, _, err := s.AddWithResult(items)
	return err
}

func (s *Store) AddWithResult(items []Account) (added, skipped int, err error) {
	s.mu.Lock()
	importedAt := s.now().Unix()
	byToken := map[string]bool{}
	for _, a := range s.accounts {
		if a.AccessToken != "" {
			byToken[a.AccessToken] = true
		}
	}
	for _, item := range items {
		item.AccessToken = strings.TrimSpace(item.AccessToken)
		item.Email = strings.TrimSpace(item.Email)
		if item.AccessToken == "" || byToken[item.AccessToken] {
			skipped++
			continue
		}
		if item.Extra == nil {
			item.Extra = map[string]any{}
		}
		item = cloneAccount(item)
		item.loadedOrder = len(s.accounts)
		if item.CreatedAt == 0 {
			item.CreatedAt = importedAt
		}
		item.ImportedAt = importedAt
		s.accounts = append(s.accounts, item)
		byToken[item.AccessToken] = true
		added++
	}
	if added > 0 {
		s.signalImageAvailabilityLocked()
	}
	if added == 0 {
		s.mu.Unlock()
		return added, skipped, nil
	}
	s.markDirtyLocked()
	snapshot, revision := s.snapshotLocked()
	s.mu.Unlock()
	err = s.persistSnapshot(snapshot, revision)
	return added, skipped, err
}

func (s *Store) Delete(tokens []string) (int, error) {
	wanted := map[string]bool{}
	for _, token := range tokens {
		if token = strings.TrimSpace(token); token != "" {
			wanted[token] = true
		}
	}
	if len(wanted) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	next := s.accounts[:0]
	removed := 0
	for _, account := range s.accounts {
		if wanted[account.AccessToken] {
			delete(s.imageLeases, account.AccessToken)
			removed++
			continue
		}
		next = append(next, account)
	}
	s.accounts = next
	if removed == 0 {
		s.mu.Unlock()
		return 0, nil
	}
	s.signalImageAvailabilityLocked()
	s.markDirtyLocked()
	snapshot, revision := s.snapshotLocked()
	s.mu.Unlock()
	return removed, s.persistSnapshot(snapshot, revision)
}

func (s *Store) Update(token string, updates map[string]any) (Account, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Account{}, false, nil
	}
	s.mu.Lock()
	for i := range s.accounts {
		if s.accounts[i].AccessToken != token {
			continue
		}
		applyUpdate(&s.accounts[i], updates)
		s.signalImageAvailabilityLocked()
		result := cloneAccount(s.accounts[i])
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		if err := s.persistSnapshot(snapshot, revision); err != nil {
			return Account{}, false, err
		}
		return result, true, nil
	}
	s.mu.Unlock()
	return Account{}, false, nil
}

func (s *Store) Export(tokens []string) []map[string]string {
	wanted := map[string]bool{}
	for _, token := range tokens {
		if token = strings.TrimSpace(token); token != "" {
			wanted[token] = true
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []map[string]string{}
	for _, account := range s.accounts {
		if len(wanted) > 0 && !wanted[account.AccessToken] {
			continue
		}
		if account.AccessToken == "" || account.RefreshToken == "" || account.IDToken == "" {
			continue
		}
		out = append(out, map[string]string{"access_token": account.AccessToken, "refresh_token": account.RefreshToken, "id_token": account.IDToken})
	}
	return out
}

func (s *Store) Get(token string) (Account, bool) {
	token = strings.TrimSpace(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.accounts {
		if account.AccessToken == token {
			return cloneAccount(account), true
		}
	}
	return Account{}, false
}

func (s *Store) Tokens() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tokens := make([]string, 0, len(s.accounts))
	for _, account := range s.accounts {
		if strings.TrimSpace(account.AccessToken) != "" {
			tokens = append(tokens, account.AccessToken)
		}
	}
	return tokens
}

// EnsureBrowserIdentities persists one browser-shaped identity per account so
// upstream requests do not rotate device or session IDs on every call.
func (s *Store) EnsureBrowserIdentities() (int, error) {
	s.mu.Lock()
	updated := 0
	for index := range s.accounts {
		changed, err := ensureBrowserIdentity(&s.accounts[index])
		if err != nil {
			s.mu.Unlock()
			return updated, err
		}
		if changed {
			updated++
		}
	}
	if updated == 0 {
		s.mu.Unlock()
		return 0, nil
	}
	s.markDirtyLocked()
	snapshot, revision := s.snapshotLocked()
	s.mu.Unlock()
	return updated, s.persistSnapshot(snapshot, revision)
}

func (s *Store) EnsureBrowserIdentity(token string) (Account, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Account{}, false, nil
	}
	s.mu.Lock()
	for index := range s.accounts {
		account := &s.accounts[index]
		if account.AccessToken != token {
			continue
		}
		changed, err := ensureBrowserIdentity(account)
		if err != nil {
			s.mu.Unlock()
			return Account{}, false, err
		}
		if changed {
			result := cloneAccount(*account)
			s.markDirtyLocked()
			snapshot, revision := s.snapshotLocked()
			s.mu.Unlock()
			if err := s.persistSnapshot(snapshot, revision); err != nil {
				return Account{}, false, err
			}
			return result, true, nil
		}
		result := cloneAccount(*account)
		s.mu.Unlock()
		return result, true, nil
	}
	s.mu.Unlock()
	return Account{}, false, nil
}

func ensureBrowserIdentity(account *Account) (bool, error) {
	changed := false
	if strings.TrimSpace(account.DeviceID) == "" {
		value, err := newBrowserUUID()
		if err != nil {
			return false, err
		}
		account.DeviceID = value
		changed = true
	}
	if strings.TrimSpace(account.SessionID) == "" {
		value, err := newBrowserUUID()
		if err != nil {
			return false, err
		}
		account.SessionID = value
		changed = true
	}
	if strings.TrimSpace(account.UserAgent) == "" && (account.FP == nil || strings.TrimSpace(account.FP["user-agent"]) == "") {
		account.UserAgent = DefaultBrowserUserAgent
		changed = true
	}
	return changed, nil
}

func newBrowserUUID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16]), nil
}

func (s *Store) RecordRefresh(token string, check AccountCheckResult, refreshErr error) (Account, bool, error) {
	token = strings.TrimSpace(token)
	s.mu.Lock()
	for i := range s.accounts {
		account := &s.accounts[i]
		if account.AccessToken != token {
			continue
		}
		if account.Extra == nil {
			account.Extra = map[string]any{}
		}
		now := s.now().In(time.Local).Format(time.RFC3339)
		account.Extra["last_account_refresh_at"] = now
		if refreshErr != nil {
			message := strings.TrimSpace(refreshErr.Error())
			account.LastError = message
			account.Extra["last_refresh_error"] = message
			account.Extra["last_refresh_error_at"] = now
			if isRateLimitMessage(message) {
				account.Status = "限流"
			} else {
				account.Status = "异常"
			}
		} else {
			applySuccessfulAccountRefresh(account, check, s.now())
		}
		s.signalImageAvailabilityLocked()
		result := cloneAccount(*account)
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		if err := s.persistSnapshot(snapshot, revision); err != nil {
			return Account{}, false, err
		}
		return result, true, nil
	}
	s.mu.Unlock()
	return Account{}, false, nil
}

func applySuccessfulAccountRefresh(account *Account, check AccountCheckResult, now time.Time) {
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	nowText := now.In(time.Local).Format(time.RFC3339)
	account.Extra["last_account_refresh_at"] = nowText
	if check.Email != "" {
		account.Email = strings.TrimSpace(check.Email)
	}
	if check.Type != "" {
		account.Type = strings.TrimSpace(check.Type)
	}
	account.Quota = max(0, check.Quota)
	account.ImageQuotaUnknown = check.ImageQuotaUnknown
	if !check.ImageQuotaUnknown {
		updateImageQuotaTotal(account, check.Quota, imageQuotaTotalFromProgress(check.LimitsProgress))
		account.Extra["image_quota_synced_at"] = nowText
		delete(account.Extra, "image_quota_refresh_required")
		delete(account.Extra, "image_quota_estimated_at")
	}
	if check.ImageQuotaUnknown || account.Quota > 0 {
		account.Status = "正常"
	} else {
		account.Status = "限流"
	}
	account.LastError = ""
	delete(account.Extra, "last_error")
	delete(account.Extra, "last_refresh_error")
	delete(account.Extra, "last_refresh_error_at")
	account.Extra["last_refresh_error"] = ""
	// Image-path prechecks intentionally skip the comparatively expensive
	// models endpoint. Preserve the last known model list unless a full account
	// refresh supplied an explicit value.
	if check.Models != nil {
		account.Extra["available_models"] = append([]string(nil), check.Models...)
	}
	account.Extra["limits_progress"] = cloneExtraValue(check.LimitsProgress)
	account.Extra["restore_at"] = check.RestoreAt
	account.Extra["default_model_slug"] = check.DefaultModelSlug
}

func (s *Store) SelectForImage(exclude map[string]bool) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.selectForImageLocked(exclude, false)
	if !ok {
		return Account{}, ErrNoAvailableAccount
	}
	return cloneAccount(account), nil
}

// AcquireForImage reserves one idle account for an image request. If all
// otherwise-eligible accounts are occupied, it waits for a release so callers
// can remain in the task queue without starting a second request on a token.
func (s *Store) AcquireForImage(ctx context.Context, exclude map[string]bool, onWait func()) (Account, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var waiter *imageWaiter
	waited := false
	for {
		if err := ctx.Err(); err != nil {
			s.removeImageWaiter(waiter)
			return Account{}, err
		}
		s.mu.Lock()
		if waiter == nil && len(s.imageWaiters) > 0 {
			waiter = s.enqueueImageWaiterLocked()
		}
		if waiter == nil || s.imageWaiters[0] == waiter {
			account, available := s.selectForImageLocked(exclude, true)
			if available {
				if waiter != nil {
					s.removeImageWaiterLocked(waiter)
				}
				s.imageLeases[account.AccessToken] = struct{}{}
				s.mu.Unlock()
				return cloneAccount(account), nil
			}
			_, eligible := s.selectForImageLocked(exclude, false)
			cooldownUntil, cooling := s.earliestImageCooldownLocked(exclude)
			if !eligible && !cooling {
				if waiter != nil {
					s.removeImageWaiterLocked(waiter)
				}
				s.mu.Unlock()
				return Account{}, ErrNoAvailableAccount
			}
			if waiter == nil {
				waiter = s.enqueueImageWaiterLocked()
			}
			changed := s.prepareImageWaiterWaitLocked(waiter)
			s.mu.Unlock()

			if !waited && onWait != nil {
				onWait()
				waited = true
			}
			if err := waitForImageAvailability(ctx, changed, cooldownUntil); err != nil {
				s.removeImageWaiter(waiter)
				return Account{}, err
			}
			continue
		}
		if waiter == nil {
			waiter = s.enqueueImageWaiterLocked()
		}
		changed := s.prepareImageWaiterWaitLocked(waiter)
		s.mu.Unlock()

		if !waited && onWait != nil {
			onWait()
			waited = true
		}
		if err := waitForImageAvailability(ctx, changed, time.Time{}); err != nil {
			s.removeImageWaiter(waiter)
			return Account{}, err
		}
	}
}

// AcquireAccountForImage reserves a specific account. This is used by the
// account image-test endpoint so it follows the same one-request-per-account
// rule as the normal dispatcher.
func (s *Store) AcquireAccountForImage(ctx context.Context, token string, onWait func()) (Account, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Account{}, ErrAccountNotFound
	}
	waited := false
	for {
		s.mu.Lock()
		var account Account
		found := false
		for _, candidate := range s.accounts {
			if candidate.AccessToken == token {
				account = candidate
				found = true
				break
			}
		}
		if !found {
			s.mu.Unlock()
			return Account{}, ErrAccountNotFound
		}
		if !usable(account) || isImageCooling(account, s.now()) {
			s.mu.Unlock()
			return Account{}, ErrNoAvailableAccount
		}
		if _, occupied := s.imageLeases[token]; !occupied {
			s.imageLeases[token] = struct{}{}
			s.mu.Unlock()
			return cloneAccount(account), nil
		}
		changed := s.imageLeaseChanged
		s.mu.Unlock()

		if !waited && onWait != nil {
			onWait()
			waited = true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return Account{}, ctx.Err()
		}
	}
}

// ReleaseImage makes an account available to the next queued image task.
func (s *Store) ReleaseImage(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, occupied := s.imageLeases[token]; !occupied {
		return
	}
	delete(s.imageLeases, token)
	s.signalImageAvailabilityLocked()
}

func (s *Store) enqueueImageWaiterLocked() *imageWaiter {
	waiter := &imageWaiter{ready: make(chan struct{})}
	s.imageWaiters = append(s.imageWaiters, waiter)
	return waiter
}

func (s *Store) prepareImageWaiterWaitLocked(waiter *imageWaiter) <-chan struct{} {
	if waiter == nil {
		return nil
	}
	waiter.ready = make(chan struct{})
	waiter.notified = false
	return waiter.ready
}

func (s *Store) removeImageWaiter(waiter *imageWaiter) {
	if waiter == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeImageWaiterLocked(waiter)
}

func (s *Store) removeImageWaiterLocked(waiter *imageWaiter) {
	if waiter == nil {
		return
	}
	for index, candidate := range s.imageWaiters {
		if candidate != waiter {
			continue
		}
		wasHead := index == 0
		copy(s.imageWaiters[index:], s.imageWaiters[index+1:])
		s.imageWaiters[len(s.imageWaiters)-1] = nil
		s.imageWaiters = s.imageWaiters[:len(s.imageWaiters)-1]
		if wasHead {
			s.wakeImageWaiterLocked()
		}
		return
	}
}

func (s *Store) wakeImageWaiterLocked() {
	if len(s.imageWaiters) == 0 {
		return
	}
	waiter := s.imageWaiters[0]
	if waiter == nil || waiter.notified {
		return
	}
	close(waiter.ready)
	waiter.notified = true
}

func (s *Store) selectForImageLocked(exclude map[string]bool, skipOccupied bool) (Account, bool) {
	now := s.now()
	var selected Account
	found := false
	for _, a := range s.accounts {
		if !usable(a) {
			continue
		}
		if isImageCooling(a, now) {
			continue
		}
		if exclude != nil && exclude[a.AccessToken] {
			continue
		}
		if skipOccupied {
			if _, occupied := s.imageLeases[a.AccessToken]; occupied {
				continue
			}
		}
		if !found || imageAccountPreferred(a, selected) {
			selected = a
			found = true
		}
	}
	return selected, found
}

// imageAccountPreferred retains the previous stable-sort ordering while
// allowing dispatch to find the best account in a single pass under s.mu.
func imageAccountPreferred(left, right Account) bool {
	leftImported := left.ImportedAt > 0
	rightImported := right.ImportedAt > 0
	if leftImported != rightImported {
		return leftImported
	}
	if leftImported && left.ImportedAt != right.ImportedAt {
		return left.ImportedAt > right.ImportedAt
	}
	if leftImported && left.LastUsedAt != right.LastUsedAt {
		return left.LastUsedAt < right.LastUsedAt
	}
	if left.CreatedAt != right.CreatedAt {
		return left.CreatedAt > right.CreatedAt
	}
	if left.loadedOrder != right.loadedOrder {
		return left.loadedOrder > right.loadedOrder
	}
	return left.Email > right.Email
}

func (s *Store) earliestImageCooldownLocked(exclude map[string]bool) (time.Time, bool) {
	now := s.now()
	var earliest time.Time
	for _, account := range s.accounts {
		if !usable(account) || (exclude != nil && exclude[account.AccessToken]) {
			continue
		}
		until := imageCooldownUntil(account)
		if !until.After(now) {
			continue
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	return earliest, !earliest.IsZero()
}

func waitForImageAvailability(ctx context.Context, changed <-chan struct{}, cooldownUntil time.Time) error {
	if cooldownUntil.IsZero() {
		select {
		case <-changed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	delay := time.Until(cooldownUntil)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-changed:
		return nil
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) signalImageAvailabilityLocked() {
	if s.imageLeaseChanged != nil {
		close(s.imageLeaseChanged)
	}
	s.imageLeaseChanged = make(chan struct{})
	s.wakeImageWaiterLocked()
}

func usable(a Account) bool {
	if strings.TrimSpace(a.AccessToken) == "" || a.Disabled {
		return false
	}
	return !isStatus(a.Status, "pending_validation", "validating", "removed", "invalid", "token_revoked", "token_invalidated", "no_quota", "deleted", "disabled", "rate_limited", "limited", "abnormal", "验证中", "检测中", "禁用", "限流", "异常", StatusCredentialInvalid, StatusCredentialRecovery)
}

func (s *Store) MarkSuccess(token string) error {
	return s.updateByToken(token, func(a *Account) {
		recordSuccess(a, s.now())
	})
}

// MarkImageSuccess records an image result and immediately updates the local
// remaining-quota estimate. A later account refresh remains authoritative.
func (s *Store) MarkImageSuccess(token string) error {
	return s.updateByToken(token, func(a *Account) {
		now := s.now()
		recordSuccess(a, now)
		if a.ImageQuotaUnknown {
			return
		}
		updateImageQuotaTotal(a, a.Quota)
		if a.Quota > 0 {
			a.Quota--
		}
		updateImageQuotaRemaining(a.Extra, a.Quota)
		a.Extra["image_quota_estimated_at"] = now.In(time.Local).Format(time.RFC3339)
		if a.Quota == 0 {
			a.Extra["image_quota_refresh_required"] = true
		}
	})
}

// MarkImageQuotaExhausted keeps the account for a later refresh instead of
// deleting a token based on an upstream quota response that may be transient.
func (s *Store) MarkImageQuotaExhausted(token string, err error) error {
	return s.updateByToken(token, func(a *Account) {
		now := s.now()
		a.LastUsedAt = now.Unix()
		a.LastError = strings.TrimSpace(fmt.Sprint(err))
		if a.Extra == nil {
			a.Extra = map[string]any{}
		}
		if !a.ImageQuotaUnknown {
			updateImageQuotaTotal(a, a.Quota)
			a.Quota = 0
			updateImageQuotaRemaining(a.Extra, 0)
		}
		a.Status = "限流"
		a.Extra["last_used_at"] = now.In(time.Local).Format(time.RFC3339)
		a.Extra["image_quota_refresh_required"] = true
		a.Extra["image_quota_limited_at"] = now.In(time.Local).Format(time.RFC3339)
	})
}

func recordSuccess(account *Account, now time.Time) {
	account.LastUsedAt = now.Unix()
	account.LastError = ""
	account.ImageOK++
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	clearImageCooldown(account.Extra)
	account.Extra["last_used_at"] = now.In(time.Local).Format(time.RFC3339)
}

func updateImageQuotaTotal(account *Account, candidates ...int) {
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	total := max(0, asInt(account.Extra["image_quota_total"]))
	for _, candidate := range candidates {
		if candidate > total {
			total = candidate
		}
	}
	if total > 0 {
		account.Extra["image_quota_total"] = total
	}
}

func imageQuotaTotalFromProgress(progress []map[string]any) int {
	for _, limit := range progress {
		if strings.TrimSpace(fmt.Sprint(limit["feature_name"])) != "image_gen" {
			continue
		}
		for _, key := range []string{"total", "limit", "max", "quota", "capacity"} {
			if value := asInt(limit[key]); value > 0 {
				return value
			}
		}
		if usage, ok := limit["usage"].(map[string]any); ok {
			for _, key := range []string{"total", "limit", "max", "quota", "capacity"} {
				if value := asInt(usage[key]); value > 0 {
					return value
				}
			}
		}
	}
	return 0
}

func updateImageQuotaRemaining(extra map[string]any, remaining int) {
	if extra == nil {
		return
	}
	update := func(limit map[string]any) bool {
		if strings.TrimSpace(fmt.Sprint(limit["feature_name"])) != "image_gen" {
			return false
		}
		limit["remaining"] = remaining
		return true
	}
	switch limits := extra["limits_progress"].(type) {
	case []map[string]any:
		for _, limit := range limits {
			if update(limit) {
				return
			}
		}
	case []any:
		for _, item := range limits {
			if limit, ok := item.(map[string]any); ok && update(limit) {
				return
			}
		}
	}
}

func (s *Store) MarkFailure(token string, err error) error {
	return s.updateByToken(token, func(a *Account) {
		a.LastUsedAt = s.now().Unix()
		a.LastError = strings.TrimSpace(fmt.Sprint(err))
		a.ImageFailures++
		if a.Extra == nil {
			a.Extra = map[string]any{}
		}
		a.Extra["last_used_at"] = s.now().In(time.Local).Format(time.RFC3339)
	})
}

// MarkImageRateLimited temporarily removes an account from image dispatch.
// A positive retryAfter is honored when it exceeds the local backoff.
func (s *Store) MarkImageRateLimited(token string, retryAfter time.Duration, err error) error {
	return s.markImageCooldown(token, ImageCooldownRateLimited, retryAfter, err)
}

// MarkImageUpstreamFailure temporarily backs off an account after a retryable
// upstream 5xx response. It does not change the account's persistent status.
func (s *Store) MarkImageUpstreamFailure(token string, err error) error {
	return s.markImageCooldown(token, ImageCooldownUpstreamFailure, 0, err)
}

// MarkImageGenerationTerminated temporarily backs off an account when the
// upstream image tool has already reached a terminal failure state.
func (s *Store) MarkImageGenerationTerminated(token string, err error) error {
	return s.markImageCooldown(token, ImageCooldownGenerationTerminated, 0, err)
}

// MarkImageHTTPFailure applies the image dispatch cooldown policy for HTTP
// failures that are known to be transient. Other status codes are ignored.
func (s *Store) MarkImageHTTPFailure(token string, statusCode int, retryAfter time.Duration, err error) error {
	switch {
	case statusCode == 429:
		return s.MarkImageRateLimited(token, retryAfter, err)
	case statusCode >= 500 && statusCode <= 599:
		return s.markImageCooldown(token, ImageCooldownUpstreamFailure, retryAfter, err)
	default:
		return nil
	}
}

func (s *Store) markImageCooldown(token string, reason ImageCooldownReason, retryAfter time.Duration, err error) error {
	return s.updateByToken(token, func(a *Account) {
		now := s.now()
		if a.Extra == nil {
			a.Extra = map[string]any{}
		}
		failures := max(0, asInt(a.Extra[imageCooldownFailuresKey])) + 1
		until := now.Add(imageCooldownDelay(reason, retryAfter, failures))

		a.LastUsedAt = now.Unix()
		a.LastError = compactImageCooldownError(err)
		a.ImageFailures++
		a.Extra["last_used_at"] = now.In(time.Local).Format(time.RFC3339)
		a.Extra[imageCooldownReasonKey] = string(reason)
		a.Extra[imageCooldownFailuresKey] = failures
		a.Extra[imageCooldownUntilKey] = until.UTC().Format(time.RFC3339Nano)
		a.Extra[imageCooldownLastErrorKey] = a.LastError
		a.Extra[imageCooldownLastAtKey] = now.UTC().Format(time.RFC3339Nano)
	})
}

func imageCooldownDelay(reason ImageCooldownReason, retryAfter time.Duration, failures int) time.Duration {
	base, capDelay := 15*time.Second, 3*time.Minute
	switch reason {
	case ImageCooldownRateLimited:
		base, capDelay = 30*time.Second, 15*time.Minute
	case ImageCooldownGenerationTerminated:
		base, capDelay = 20*time.Second, 5*time.Minute
	}
	for attempts := max(0, failures-1); attempts > 0 && base < capDelay; attempts-- {
		base *= 2
	}
	if base > capDelay {
		base = capDelay
	}
	if retryAfter > base {
		return retryAfter
	}
	return base
}

func compactImageCooldownError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

func isImageCooling(account Account, now time.Time) bool {
	return imageCooldownUntil(account).After(now)
}

func imageCooldownUntil(account Account) time.Time {
	if account.Extra == nil {
		return time.Time{}
	}
	value, ok := account.Extra[imageCooldownUntilKey]
	if !ok || value == nil {
		return time.Time{}
	}
	switch typed := value.(type) {
	case time.Time:
		return typed
	case int64:
		return time.Unix(typed, 0)
	case int:
		return time.Unix(int64(typed), 0)
	case float64:
		return time.Unix(int64(typed), 0)
	}
	raw := strings.TrimSpace(fmt.Sprint(value))
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed
		}
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(seconds, 0)
	}
	return time.Time{}
}

func clearImageCooldown(extra map[string]any) {
	for _, key := range []string{
		imageCooldownUntilKey,
		imageCooldownReasonKey,
		imageCooldownFailuresKey,
		imageCooldownLastErrorKey,
		imageCooldownLastAtKey,
	} {
		delete(extra, key)
	}
}

// PendingTokenRecoveries returns accounts whose failed credentials are ready
// for an asynchronous OAuth refresh attempt.
func (s *Store) PendingTokenRecoveries(now time.Time) []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Account, 0)
	for _, account := range s.accounts {
		if !isStatus(account.Status, StatusCredentialInvalid) || !tokenRecoveryIsDue(account, now) {
			continue
		}
		items = append(items, cloneAccount(account))
	}
	return items
}

// BeginTokenRecovery reserves a pending credential recovery attempt. The
// status remains outside the dispatch pool until CompleteTokenRecovery runs.
func (s *Store) BeginTokenRecovery(token string, now time.Time) (Account, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Account{}, false, nil
	}
	s.mu.Lock()
	for index := range s.accounts {
		account := &s.accounts[index]
		if account.AccessToken != token || !isStatus(account.Status, StatusCredentialInvalid) || !tokenRecoveryIsDue(*account, now) {
			continue
		}
		if account.Extra == nil {
			account.Extra = map[string]any{}
		}
		account.Status = StatusCredentialRecovery
		account.Extra[tokenRecoveryStateKey] = tokenRecoveryRunning
		account.Extra["token_recovery_last_started_at"] = now.In(time.Local).Format(time.RFC3339)
		s.appendCredentialRecoveryLogLocked(
			*account,
			"processing",
			"recovery_started",
			fmt.Sprintf("开始第 %d 次后台凭证恢复", asInt(account.Extra[tokenRecoveryAttemptsKey])+1),
			"",
			asInt(account.Extra[tokenRecoveryAttemptsKey])+1,
		)
		result := cloneAccount(*account)
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		if err := s.persistSnapshot(snapshot, revision); err != nil {
			return Account{}, false, err
		}
		return result, true, nil
	}
	s.mu.Unlock()
	return Account{}, false, nil
}

// ReplaceOAuthTokens stores a successful OAuth refresh before the new token is
// validated. Empty refresh/id token values retain the prior values because
// refresh responses are allowed to omit non-rotated credentials.
func (s *Store) ReplaceOAuthTokens(token, accessToken, refreshToken, idToken string) (Account, bool, error) {
	return s.replaceOAuthTokens(token, accessToken, refreshToken, idToken, "token_refreshed", "OAuth Token 刷新成功，正在验证新凭证")
}

// ReplaceOAuthTokensAfterPasswordLogin stores credentials obtained through the
// password and mailbox fallback. It has a distinct recovery log event so the
// operator can tell it apart from a regular refresh-token exchange.
func (s *Store) ReplaceOAuthTokensAfterPasswordLogin(token, accessToken, refreshToken, idToken string) (Account, bool, error) {
	return s.replaceOAuthTokens(token, accessToken, refreshToken, idToken, "password_relogin_succeeded", "密码重新登录成功，已获取新 Token，正在验证新凭证")
}

func (s *Store) replaceOAuthTokens(token, accessToken, refreshToken, idToken, event, message string) (Account, bool, error) {
	token = strings.TrimSpace(token)
	accessToken = strings.TrimSpace(accessToken)
	if token == "" || accessToken == "" {
		return Account{}, false, fmt.Errorf("access token is required")
	}
	s.mu.Lock()
	for index := range s.accounts {
		if s.accounts[index].AccessToken != token {
			continue
		}
		for otherIndex, other := range s.accounts {
			if otherIndex != index && other.AccessToken == accessToken {
				s.mu.Unlock()
				return Account{}, false, fmt.Errorf("refreshed access token already belongs to another account")
			}
		}
		account := &s.accounts[index]
		account.AccessToken = accessToken
		if value := strings.TrimSpace(refreshToken); value != "" {
			account.RefreshToken = value
		}
		if value := strings.TrimSpace(idToken); value != "" {
			account.IDToken = value
		}
		if account.Extra == nil {
			account.Extra = map[string]any{}
		}
		account.Status = StatusCredentialRecovery
		account.Extra[tokenRecoveryStateKey] = tokenRecoveryRunning
		account.Extra["token_recovery_token_refreshed_at"] = s.now().In(time.Local).Format(time.RFC3339)
		s.appendCredentialRecoveryLogLocked(
			*account,
			"processing",
			event,
			message,
			"",
			asInt(account.Extra[tokenRecoveryAttemptsKey])+1,
		)
		delete(s.imageLeases, token)
		s.signalImageAvailabilityLocked()
		result := cloneAccount(*account)
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		if err := s.persistSnapshot(snapshot, revision); err != nil {
			return result, true, err
		}
		return result, true, nil
	}
	s.mu.Unlock()
	return Account{}, false, nil
}

// LogTokenRecoveryEvent persists an additional background recovery phase for
// the current account. It never records credential values.
func (s *Store) LogTokenRecoveryEvent(token, level, event, message, errText string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	s.mu.Lock()
	for index := range s.accounts {
		account := &s.accounts[index]
		if account.AccessToken != token {
			continue
		}
		if account.Extra == nil {
			account.Extra = map[string]any{}
		}
		s.appendCredentialRecoveryLogLocked(
			*account,
			level,
			event,
			message,
			errText,
			asInt(account.Extra[tokenRecoveryAttemptsKey])+1,
		)
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		return true, s.persistSnapshot(snapshot, revision)
	}
	s.mu.Unlock()
	return false, nil
}

// CompleteTokenRecovery validates the refreshed credentials and makes the
// account available for dispatch again.
func (s *Store) CompleteTokenRecovery(token string, check AccountCheckResult) (Account, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Account{}, false, nil
	}
	s.mu.Lock()
	for index := range s.accounts {
		account := &s.accounts[index]
		if account.AccessToken != token {
			continue
		}
		attempt := asInt(account.Extra[tokenRecoveryAttemptsKey]) + 1
		applySuccessfulAccountRefresh(account, check, s.now())
		clearTokenRecoveryMetadata(account.Extra)
		account.Extra["token_recovery_recovered_at"] = s.now().In(time.Local).Format(time.RFC3339)
		s.appendCredentialRecoveryLogLocked(
			*account,
			"success",
			"recovery_succeeded",
			"凭证恢复成功，账号已恢复调度",
			"",
			attempt,
		)
		s.signalImageAvailabilityLocked()
		result := cloneAccount(*account)
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		if err := s.persistSnapshot(snapshot, revision); err != nil {
			return result, true, err
		}
		return result, true, nil
	}
	s.mu.Unlock()
	return Account{}, false, nil
}

// FailTokenRecovery schedules a later recovery attempt. The configured final
// failure removes the account from the pool after background recovery fails.
func (s *Store) FailTokenRecovery(token, reason string, maxAttempts int, retryAfter time.Duration) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if retryAfter <= 0 {
		retryAfter = time.Minute
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "OAuth credential recovery failed"
	}

	s.mu.Lock()
	for index := range s.accounts {
		account := &s.accounts[index]
		if account.AccessToken != token {
			continue
		}
		if account.Extra == nil {
			account.Extra = map[string]any{}
		}
		attempts := asInt(account.Extra[tokenRecoveryAttemptsKey]) + 1
		account.Extra[tokenRecoveryAttemptsKey] = attempts
		account.LastError = reason
		account.Extra["token_recovery_last_error"] = reason
		account.Extra["token_recovery_last_error_at"] = s.now().In(time.Local).Format(time.RFC3339)
		if attempts >= maxAttempts {
			s.appendCredentialRecoveryLogLocked(
				*account,
				"error",
				"recovery_deleted",
				fmt.Sprintf("第 %d 次后台凭证恢复失败，账号已自动删除", attempts),
				reason,
				attempts,
			)
			s.accounts = append(s.accounts[:index], s.accounts[index+1:]...)
			delete(s.imageLeases, token)
			s.signalImageAvailabilityLocked()
			s.markDirtyLocked()
			snapshot, revision := s.snapshotLocked()
			s.mu.Unlock()
			return true, s.persistSnapshot(snapshot, revision)
		}
		account.Status = StatusCredentialInvalid
		account.Extra[tokenRecoveryStateKey] = tokenRecoveryPending
		account.Extra[tokenRecoveryNextAtKey] = s.now().Add(retryAfter).In(time.Local).Format(time.RFC3339)
		delete(account.Extra, "token_recovery_last_started_at")
		s.appendCredentialRecoveryLogLocked(
			*account,
			"warning",
			"recovery_failed",
			fmt.Sprintf("第 %d 次后台凭证恢复失败，等待下一次后台重试", attempts),
			reason,
			attempts,
		)
		s.signalImageAvailabilityLocked()
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		return false, s.persistSnapshot(snapshot, revision)
	}
	s.mu.Unlock()
	return false, nil
}

func tokenRecoveryIsDue(account Account, now time.Time) bool {
	if account.Extra == nil || strings.TrimSpace(fmt.Sprint(account.Extra[tokenRecoveryStateKey])) != tokenRecoveryPending {
		return false
	}
	raw := strings.TrimSpace(fmt.Sprint(account.Extra[tokenRecoveryNextAtKey]))
	if raw == "" {
		return true
	}
	due, err := time.Parse(time.RFC3339, raw)
	return err != nil || !due.After(now)
}

func clearTokenRecoveryMetadata(extra map[string]any) {
	if extra == nil {
		return
	}
	for _, key := range []string{
		tokenRecoveryStateKey,
		tokenRecoveryAttemptsKey,
		tokenRecoveryNextAtKey,
		"token_recovery_reason",
		"token_recovery_marked_at",
		"token_recovery_last_started_at",
		"token_recovery_token_refreshed_at",
		"token_recovery_last_error",
		"token_recovery_last_error_at",
	} {
		delete(extra, key)
	}
}

func (s *Store) RemoveInvalidToken(token, reason string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	s.mu.Lock()
	next := s.accounts[:0]
	removed := false
	for _, a := range s.accounts {
		if a.AccessToken == token {
			removed = true
			s.appendCredentialRecoveryLogLocked(a, "warning", "account_deleted", "账号因凭证验证失败被自动移除", reason, 0)
			continue
		}
		next = append(next, a)
	}
	s.accounts = next
	if removed {
		delete(s.imageLeases, token)
		s.signalImageAvailabilityLocked()
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		return true, s.persistSnapshot(snapshot, revision)
	}
	s.mu.Unlock()
	return false, nil
}

func (s *Store) updateByToken(token string, fn func(*Account)) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	s.mu.Lock()
	for i := range s.accounts {
		if s.accounts[i].AccessToken == token {
			fn(&s.accounts[i])
			s.markDirtyLocked()
			s.mu.Unlock()
			s.signalPersistence()
			return nil
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Store) appendCredentialRecoveryLogLocked(account Account, level, event, message, errText string, attempt int) {
	if s.now == nil {
		s.now = time.Now
	}
	s.credentialRecoverySequence++
	entry := CredentialRecoveryLog{
		ID:           fmt.Sprintf("credential_recovery_%d_%d", s.now().UnixNano(), s.credentialRecoverySequence),
		Time:         s.now().In(time.Local).Format(time.RFC3339),
		Level:        strings.TrimSpace(level),
		Event:        strings.TrimSpace(event),
		AccountEmail: strings.TrimSpace(account.Email),
		Attempt:      attempt,
		Message:      compactCredentialRecoveryLogText(message),
		Error:        compactCredentialRecoveryLogText(errText),
	}
	s.credentialRecoveryLogs = append(s.credentialRecoveryLogs, entry)
	if len(s.credentialRecoveryLogs) > maxCredentialRecoveryLogs {
		s.credentialRecoveryLogs = append([]CredentialRecoveryLog(nil), s.credentialRecoveryLogs[len(s.credentialRecoveryLogs)-maxCredentialRecoveryLogs:]...)
	}
}

func compactCredentialRecoveryLogText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 500 {
		return value
	}
	return value[:500] + "..."
}

func (s *Store) markDirtyLocked() {
	s.revision++
	s.dirty = true
}

func (s *Store) snapshotLocked() (fileShape, uint64) {
	accounts := make([]Account, len(s.accounts))
	for index := range s.accounts {
		accounts[index] = cloneAccount(s.accounts[index])
	}
	logs := append([]CredentialRecoveryLog(nil), s.credentialRecoveryLogs...)
	return fileShape{Accounts: accounts, CredentialRecoveryLogs: logs}, s.revision
}

func (s *Store) signalPersistence() {
	if s == nil || s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Store) persistenceLoop() {
	defer close(s.done)
	for {
		select {
		case <-s.wake:
			timer := time.NewTimer(accountPersistDebounce)
			select {
			case <-timer.C:
				_ = s.persistPending()
			case <-s.stop:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				_ = s.persistPending()
				return
			}
		case <-s.stop:
			_ = s.persistPending()
			return
		}
	}
}

// Flush persists all account updates accepted in memory before it returns.
// Normal image-result updates use the debounce worker so storage latency never
// holds the dispatch lock; Flush is for tests and graceful shutdown.
func (s *Store) Flush() error {
	if s == nil {
		return nil
	}
	for {
		if err := s.persistPending(); err != nil {
			return err
		}
		s.mu.RLock()
		dirty := s.dirty
		s.mu.RUnlock()
		if !dirty {
			return nil
		}
	}
}

// Close stops the account persistence worker after flushing accepted updates.
// The shared persistence.Store remains owned by the application.
func (s *Store) Close() {
	if s == nil || s.stop == nil {
		return
	}
	s.close.Do(func() {
		close(s.stop)
		<-s.done
		_ = s.Flush()
	})
}

func (s *Store) persistPending() error {
	if s == nil {
		return nil
	}
	s.persist.Lock()
	defer s.persist.Unlock()

	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	snapshot, revision := s.snapshotLocked()
	s.mu.Unlock()
	return s.saveSnapshot(snapshot, revision)
}

func (s *Store) persistSnapshot(snapshot fileShape, revision uint64) error {
	s.persist.Lock()
	defer s.persist.Unlock()
	return s.saveSnapshot(snapshot, revision)
}

// saveSnapshot is called with s.persist held and never holds s.mu while I/O
// runs. The revision check leaves a concurrent update dirty for a later write
// instead of acknowledging an obsolete snapshot.
func (s *Store) saveSnapshot(snapshot fileShape, revision uint64) error {
	err := s.save(snapshot)
	s.mu.Lock()
	if err == nil && s.revision == revision {
		s.dirty = false
	} else {
		s.dirty = true
	}
	more := s.dirty
	s.mu.Unlock()
	if err == nil && more {
		s.signalPersistence()
	}
	return err
}

func (s *Store) save(shaped fileShape) error {
	if s.state != nil {
		return s.state.Save(context.Background(), "accounts", shaped)
	}
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(shaped, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func applyUpdate(account *Account, updates map[string]any) {
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	for key, value := range updates {
		switch key {
		case "type":
			account.Type = strings.TrimSpace(fmt.Sprint(value))
		case "status":
			account.Status = strings.TrimSpace(fmt.Sprint(value))
		case "quota":
			account.Quota = asInt(value)
		case "proxy":
			account.Proxy = strings.TrimSpace(fmt.Sprint(value))
		case "email":
			account.Email = strings.TrimSpace(fmt.Sprint(value))
		case "password":
			account.Password = strings.TrimSpace(fmt.Sprint(value))
		case "disabled":
			account.Disabled = asBool(value)
		default:
			account.Extra[key] = cloneExtraValue(value)
		}
	}
}

func rawString(raw map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		var str string
		if json.Unmarshal(value, &str) == nil {
			return strings.TrimSpace(str)
		}
		var number json.Number
		if json.Unmarshal(value, &number) == nil {
			return number.String()
		}
	}
	return ""
}

func rawBool(raw map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		var boolValue bool
		if json.Unmarshal(value, &boolValue) == nil {
			return boolValue
		}
		return asBool(rawString(raw, key))
	}
	return false
}

func rawInt(raw map[string]json.RawMessage, keys ...string) int {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		var number json.Number
		if json.Unmarshal(value, &number) == nil {
			if result, err := strconv.Atoi(number.String()); err == nil {
				return result
			}
		}
		return asInt(rawString(raw, key))
	}
	return 0
}

func rawUnix(raw map[string]json.RawMessage, keys ...string) int64 {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		var number json.Number
		if json.Unmarshal(value, &number) == nil {
			if result, err := strconv.ParseInt(number.String(), 10, 64); err == nil {
				return result
			}
		}
		valueText := rawString(raw, key)
		if result, err := strconv.ParseInt(valueText, 10, 64); err == nil {
			return result
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, valueText); err == nil {
				return parsed.Unix()
			}
		}
	}
	return 0
}

func rawStringMap(raw map[string]json.RawMessage, keys ...string) map[string]string {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		var result map[string]string
		if json.Unmarshal(value, &result) == nil {
			return result
		}
	}
	return nil
}

func cloneMap(source map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range source {
		out[key] = value
	}
	return out
}

func cloneAccount(account Account) Account {
	if account.FP != nil {
		fp := make(map[string]string, len(account.FP))
		for key, value := range account.FP {
			fp[key] = value
		}
		account.FP = fp
	}
	account.Extra = cloneExtraMap(account.Extra)
	return account
}

func cloneExtraMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = cloneExtraValue(value)
	}
	return out
}

// cloneExtraValue preserves concrete JSON-shaped map and slice types. Account
// data is accepted from imports as well as JSON, so it may contain []map values
// rather than only []any values produced by encoding/json.
func cloneExtraValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneExtraReflect(reflect.ValueOf(value)).Interface()
}

func cloneExtraReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		return cloneExtraReflect(value.Elem())
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneExtraReflect(iter.Value()))
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			out.Index(index).Set(cloneExtraReflect(value.Index(index)))
		}
		return out
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloneExtraReflect(value.Elem()))
		return out
	default:
		return value
	}
}

func setString(out map[string]any, key, value string) {
	if value != "" {
		out[key] = value
	}
}

func hasKey(out map[string]any, key string) bool {
	_, ok := out[key]
	return ok
}

func timestampValue(previous any, unix int64) any {
	if previous != nil && rawUnixFromAny(previous) == unix {
		return previous
	}
	return time.Unix(unix, 0).In(time.Local).Format(time.RFC3339)
}

func rawUnixFromAny(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		if result, err := strconv.ParseInt(typed, 10, 64); err == nil {
			return result
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.Unix()
			}
		}
	}
	return 0
}

func asInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		result, _ := typed.Int64()
		return int(result)
	default:
		result, _ := strconv.Atoi(strings.TrimSpace(fmt.Sprint(value)))
		return result
	}
}

func asBool(value any) bool {
	if typed, ok := value.(bool); ok {
		return typed
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(value))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func isStatus(value string, values ...string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range values {
		if value == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func isRateLimitMessage(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "rate limit") || strings.Contains(value, "429") || strings.Contains(value, "quota") || strings.Contains(value, "限流")
}

func sumImageOK(items []Account) int {
	total := 0
	for _, item := range items {
		total += item.ImageOK
	}
	return total
}

func sumImageFailures(items []Account) int {
	total := 0
	for _, item := range items {
		total += item.ImageFailures
	}
	return total
}
