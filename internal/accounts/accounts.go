package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
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
	ErrNoAvailableAccount  = errors.New("no available account")
	ErrAccountNotFound     = errors.New("account not found")
	ErrImageAccountEvicted = errors.New("image account evicted")
)

func IsImageAccountEvicted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return errors.Is(context.Cause(ctx), ErrImageAccountEvicted)
}

func imageAccountEvictedError(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrImageAccountEvicted
	}
	return fmt.Errorf("%w: %s", ErrImageAccountEvicted, reason)
}

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
	ImageCooldownGenerationStalled    ImageCooldownReason = "generation_stalled"

	imageCooldownUntilKey     = "image_cooldown_until"
	imageCooldownReasonKey    = "image_cooldown_reason"
	imageCooldownFailuresKey  = "image_cooldown_failures"
	imageCooldownLastErrorKey = "image_cooldown_last_error"
	imageCooldownLastAtKey    = "image_cooldown_last_at"

	imageReferenceUploadCooldownUntilKey     = "image_reference_upload_cooldown_until"
	imageReferenceUploadCooldownReasonKey    = "image_reference_upload_cooldown_reason"
	imageReferenceUploadCooldownLastErrorKey = "image_reference_upload_cooldown_last_error"
	imageReferenceUploadCooldownLastAtKey    = "image_reference_upload_cooldown_last_at"
	defaultImageReferenceUploadCooldown      = 24 * time.Hour
)

// ImageDispatchRequirements describes capabilities required by one image
// request. A reference-image request needs the account's file-upload path;
// plain text-to-image requests do not.
type ImageDispatchRequirements struct {
	NeedsReferenceUpload bool
}

// ImageLease is the account-scoped reservation held by one image attempt.
// The context is canceled when the account is evicted, so parallel requests
// using the same account stop together instead of waiting for their task
// deadline independently.
type ImageLease struct {
	ID      string
	Token   string
	Account Account
	Context context.Context
}

type imageLeaseRecord struct {
	ID        string
	Token     string
	cancel    context.CancelCauseFunc
	counted   bool
	createdAt time.Time
}

type imageAccountRuntime struct {
	EffectiveLimit       int
	Successes            int
	Failures             int
	ConsecutiveSuccesses int
	ConsecutiveFailures  int
	HealthScore          float64
	LastReason           string
	LastSuccessAt        time.Time
	LastFailureAt        time.Time
}

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

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func accountTokenHash(token string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return fmt.Sprintf("%x", digest[:6])
}

type Store struct {
	mu                         sync.RWMutex
	persist                    sync.Mutex
	path                       string
	state                      persistence.Store
	accounts                   []Account
	credentialRecoveryLogs     []CredentialRecoveryLog
	credentialRecoverySequence uint64
	imageLeases                map[string]int
	imageLeaseRecords          map[string]*imageLeaseRecord
	imageAccountRuntime        map[string]*imageAccountRuntime
	imageLeaseSequence         uint64
	imageMaxInflightPerAccount int
	imageDynamicSlots          bool
	imageDynamicInitialLimit   int
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

type ImageDispatchStats struct {
	Total                      int        `json:"total"`
	Usable                     int        `json:"usable"`
	Dispatchable               int        `json:"dispatchable"`
	Idle                       int        `json:"idle"`
	Leased                     int        `json:"leased"`
	Saturated                  int        `json:"saturated"`
	MaxInflightPerAccount      int        `json:"max_inflight_per_account"`
	DynamicLimitMin            int        `json:"dynamic_limit_min"`
	DynamicLimitMax            int        `json:"dynamic_limit_max"`
	DynamicSlots               int        `json:"dynamic_slots"`
	DispatchableSlots          int        `json:"dispatchable_slots"`
	IdleSlots                  int        `json:"idle_slots"`
	LeasedSlots                int        `json:"leased_slots"`
	Cooling                    int        `json:"cooling"`
	ReferenceUploadCooling     int        `json:"reference_upload_cooling"`
	Limited                    int        `json:"limited"`
	Invalid                    int        `json:"invalid"`
	Recovering                 int        `json:"recovering"`
	Abnormal                   int        `json:"abnormal"`
	Disabled                   int        `json:"disabled"`
	Dead                       int        `json:"dead"`
	KnownRemainingQuota        int        `json:"known_remaining_quota"`
	KnownQuotaAccounts         int        `json:"known_quota_accounts"`
	QuotaExhausted             int        `json:"quota_exhausted"`
	UnknownQuotaUsable         int        `json:"unknown_quota_usable"`
	TotalImageSuccess          int        `json:"total_image_success"`
	TotalImageFailures         int        `json:"total_image_failures"`
	HistoricalSuccessRate      float64    `json:"historical_success_rate"`
	HistoricalFailureRate      float64    `json:"historical_failure_rate"`
	DeadRate                   float64    `json:"dead_rate"`
	UnavailableRate            float64    `json:"unavailable_rate"`
	CoolingRate                float64    `json:"cooling_rate"`
	DispatchableRate           float64    `json:"dispatchable_rate"`
	NextCooldownEndsAt         *time.Time `json:"next_cooldown_ends_at,omitempty"`
	AverageKnownRemainingQuota float64    `json:"average_known_remaining_quota"`
}

type imageWaiter struct {
	ready    chan struct{}
	notified bool
}

type fileShape struct {
	Accounts               []Account               `json:"accounts"`
	CredentialRecoveryLogs []CredentialRecoveryLog `json:"credential_recovery_logs,omitempty"`
}

func NormalizeImageMaxInflightPerAccount(value int) int {
	if value <= 0 {
		return 1
	}
	if value > 20 {
		return 20
	}
	return value
}

func (s *Store) SetImageMaxInflightPerAccount(value int) {
	if s == nil {
		return
	}
	normalized := NormalizeImageMaxInflightPerAccount(value)
	s.mu.Lock()
	if s.imageMaxInflightPerAccount != normalized {
		s.imageMaxInflightPerAccount = normalized
		s.imageDynamicInitialLimit = normalized
		for token, runtime := range s.imageAccountRuntime {
			if runtime == nil {
				continue
			}
			if runtime.EffectiveLimit <= 0 || runtime.EffectiveLimit > normalized {
				runtime.EffectiveLimit = 1
			}
			log.Printf("image_account_slot_config event=limit_updated account_token_hash=%s effective_limit=%d ceiling=%d", accountTokenHash(token), runtime.EffectiveLimit, normalized)
		}
		s.signalImageAvailabilityLocked()
	}
	s.mu.Unlock()
}

// SetImageDynamicSlots selects whether account slots warm up and back off
// independently (dynamic) or always use the configured per-account ceiling
// (static). Existing runtime accounts are updated immediately.
func (s *Store) SetImageDynamicSlots(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	previous := s.imageDynamicSlots
	s.imageDynamicSlots = enabled
	ceiling := NormalizeImageMaxInflightPerAccount(s.imageMaxInflightPerAccount)
	target := ceiling
	if enabled {
		target = 1
	}
	s.imageDynamicInitialLimit = target
	for token, runtime := range s.imageAccountRuntime {
		if runtime == nil {
			continue
		}
		oldLimit := runtime.EffectiveLimit
		runtime.EffectiveLimit = target
		runtime.ConsecutiveSuccesses = 0
		if previous != enabled || oldLimit != target {
			log.Printf("image_account_slot_config event=mode_updated account_token_hash=%s mode=%s effective_limit=%d ceiling=%d", accountTokenHash(token), imageSlotMode(enabled), target, ceiling)
		}
	}
	s.signalImageAvailabilityLocked()
	s.mu.Unlock()
}

// ImageDynamicSlots reports whether per-account slots use the adaptive policy.
func (s *Store) ImageDynamicSlots() bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.imageDynamicSlots
}

// ResetImageDynamicLimits resets every account to the configured mode's
// starting slot. Dynamic mode starts conservatively at one; static mode starts
// at the configured per-account ceiling.
func (s *Store) ResetImageDynamicLimits() {
	if s == nil {
		return
	}
	s.mu.Lock()
	limit := NormalizeImageMaxInflightPerAccount(s.imageMaxInflightPerAccount)
	if s.imageDynamicSlots {
		limit = 1
	}
	s.imageDynamicInitialLimit = limit
	for _, runtime := range s.imageAccountRuntime {
		if runtime != nil {
			runtime.EffectiveLimit = limit
			runtime.ConsecutiveSuccesses = 0
		}
	}
	s.signalImageAvailabilityLocked()
	s.mu.Unlock()
}

func (s *Store) ImageMaxInflightPerAccount() int {
	if s == nil {
		return 1
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return NormalizeImageMaxInflightPerAccount(s.imageMaxInflightPerAccount)
}

// ImageAccountLeaseStats returns the live account-scoped slot state used by
// image dispatch. It is intentionally a snapshot: callers use it for logs and
// diagnostics, while lease admission remains the authoritative operation.
func (s *Store) ImageAccountLeaseStats(token string) (active, effectiveLimit, configuredCeiling int) {
	if s == nil {
		return 0, 0, 0
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.accountByTokenLocked(token); !found {
		return 0, 0, NormalizeImageMaxInflightPerAccount(s.imageMaxInflightPerAccount)
	}
	configuredCeiling = NormalizeImageMaxInflightPerAccount(s.imageMaxInflightPerAccount)
	effectiveLimit = s.imageEffectiveLimitLocked(token)
	active = max(0, s.imageLeases[token])
	return active, effectiveLimit, configuredCeiling
}

func (s *Store) imageRuntimeLocked(token string) *imageAccountRuntime {
	if s.imageAccountRuntime == nil {
		s.imageAccountRuntime = map[string]*imageAccountRuntime{}
	}
	ceiling := NormalizeImageMaxInflightPerAccount(s.imageMaxInflightPerAccount)
	runtime := s.imageAccountRuntime[token]
	if runtime == nil {
		initial := s.imageDynamicInitialLimit
		if !s.imageDynamicSlots {
			initial = ceiling
		}
		if initial <= 0 {
			initial = 1
		}
		runtime = &imageAccountRuntime{EffectiveLimit: initial, HealthScore: 0.5}
		s.imageAccountRuntime[token] = runtime
	}
	if !s.imageDynamicSlots {
		runtime.EffectiveLimit = ceiling
	} else if runtime.EffectiveLimit <= 0 || runtime.EffectiveLimit > ceiling {
		runtime.EffectiveLimit = 1
	}
	return runtime
}

func (s *Store) imageEffectiveLimitLocked(token string) int {
	ceiling := NormalizeImageMaxInflightPerAccount(s.imageMaxInflightPerAccount)
	if !s.imageDynamicSlots {
		return ceiling
	}
	runtime := s.imageRuntimeLocked(token)
	return max(1, minImageInt(runtime.EffectiveLimit, ceiling))
}

func imageSlotMode(dynamic bool) string {
	if dynamic {
		return "dynamic"
	}
	return "static"
}

func minImageInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (s *Store) newImageLeaseLocked(ctx context.Context, account Account) ImageLease {
	if ctx == nil {
		ctx = context.Background()
	}
	leaseCtx, cancel := context.WithCancelCause(ctx)
	s.imageLeaseSequence++
	leaseID := fmt.Sprintf("imglease_%d_%d", s.now().UnixNano(), s.imageLeaseSequence)
	if s.imageLeaseRecords == nil {
		s.imageLeaseRecords = map[string]*imageLeaseRecord{}
	}
	s.imageLeaseRecords[leaseID] = &imageLeaseRecord{ID: leaseID, Token: account.AccessToken, cancel: cancel, counted: true, createdAt: s.now()}
	s.imageLeases[account.AccessToken]++
	return ImageLease{ID: leaseID, Token: account.AccessToken, Account: cloneAccount(account), Context: leaseCtx}
}

// TryAcquireImageLeaseWithRequirements reserves one account slot and returns
// an independently cancellable lease. The global limiter remains outside this
// type; this method only changes account-level capacity.
func (s *Store) TryAcquireImageLeaseWithRequirements(ctx context.Context, exclude map[string]bool, requirements ImageDispatchRequirements) (ImageLease, bool, error) {
	if s == nil {
		return ImageLease{}, false, ErrNoAvailableAccount
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ImageLease{}, false, err
	}
	s.mu.Lock()
	account, available := s.selectForImageLocked(exclude, true, requirements)
	if available {
		lease := s.newImageLeaseLocked(ctx, account)
		active := s.imageLeases[account.AccessToken]
		limit := s.imageEffectiveLimitLocked(account.AccessToken)
		s.mu.Unlock()
		log.Printf("image_account_lease event=acquired lease_id=%s account=%s active=%d limit=%d", lease.ID, accountLogIdentity(account), active, limit)
		return lease, true, nil
	}
	_, eligible := s.selectForImageLocked(exclude, false, requirements)
	_, cooling := s.earliestImageCooldownLocked(exclude, requirements)
	s.mu.Unlock()
	if !eligible && !cooling {
		return ImageLease{}, false, ErrNoAvailableAccount
	}
	return ImageLease{}, false, nil
}

// AcquireAccountImageLease reserves a specific account slot with the same
// cancellable lease semantics used by normal dispatch.
func (s *Store) AcquireAccountImageLease(ctx context.Context, token string, requirements ImageDispatchRequirements, onWait func()) (ImageLease, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return ImageLease{}, ErrAccountNotFound
	}
	if ctx == nil {
		ctx = context.Background()
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
			return ImageLease{}, ErrAccountNotFound
		}
		if !usable(account) || isImageDispatchBlocked(account, requirements, s.now()) {
			s.mu.Unlock()
			return ImageLease{}, ErrNoAvailableAccount
		}
		limit := s.imageEffectiveLimitLocked(token)
		if s.imageLeases[token] < limit {
			lease := s.newImageLeaseLocked(ctx, account)
			active := s.imageLeases[token]
			s.mu.Unlock()
			log.Printf("image_account_lease event=acquired_specific lease_id=%s account=%s active=%d limit=%d", lease.ID, accountLogIdentity(account), active, limit)
			return lease, nil
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
			return ImageLease{}, ctx.Err()
		}
	}
}

// ReleaseImageLease releases exactly one lease. A late completion from an
// evicted account therefore cannot decrement a newer lease for the same token.
func (s *Store) ReleaseImageLease(leaseID string) {
	leaseID = strings.TrimSpace(leaseID)
	if s == nil || leaseID == "" {
		return
	}
	var cancel context.CancelCauseFunc
	var token string
	var active, limit int
	s.mu.Lock()
	record := s.imageLeaseRecords[leaseID]
	if record == nil {
		s.mu.Unlock()
		return
	}
	delete(s.imageLeaseRecords, leaseID)
	token = record.Token
	cancel = record.cancel
	if record.counted && s.imageLeases[token] > 0 {
		s.imageLeases[token]--
		if s.imageLeases[token] == 0 {
			delete(s.imageLeases, token)
		}
	}
	active = s.imageLeases[token]
	limit = s.imageEffectiveLimitLocked(token)
	s.signalImageAvailabilityLocked()
	s.mu.Unlock()
	if cancel != nil {
		cancel(nil)
	}
	log.Printf("image_account_lease event=released lease_id=%s account_token_hash=%s active=%d limit=%d", leaseID, accountTokenHash(token), active, limit)
}

// EvictImageAccount cancels all active image leases for an account. It is
// intentionally separate from persistence so callers can use it for account
// state transitions without exposing credentials in logs.
func (s *Store) EvictImageAccount(token string, reason error) {
	token = strings.TrimSpace(token)
	if s == nil || token == "" {
		return
	}
	if reason == nil {
		reason = ErrImageAccountEvicted
	} else if !errors.Is(reason, ErrImageAccountEvicted) {
		reason = fmt.Errorf("%w: %s", ErrImageAccountEvicted, compactAccountLogError(reason))
	}
	s.mu.Lock()
	cancelers := s.evictImageLeasesLocked(token)
	s.signalImageAvailabilityLocked()
	s.mu.Unlock()
	for _, cancel := range cancelers {
		if cancel != nil {
			cancel(reason)
		}
	}
	log.Printf("image_account_lease event=evicted account_token_hash=%s active_cancelled=%d reason=%s", accountTokenHash(token), len(cancelers), compactAccountLogError(reason))
}

func cancelImageLeaseContexts(cancelers []context.CancelCauseFunc, reason error) {
	for _, cancel := range cancelers {
		if cancel != nil {
			cancel(reason)
		}
	}
}

func (s *Store) evictImageLeasesLocked(token string) []context.CancelCauseFunc {
	if s.imageLeases == nil {
		s.imageLeases = map[string]int{}
	}
	if s.imageLeaseRecords == nil {
		s.imageLeaseRecords = map[string]*imageLeaseRecord{}
	}
	cancelers := make([]context.CancelCauseFunc, 0)
	for leaseID, record := range s.imageLeaseRecords {
		if record == nil || record.Token != token {
			continue
		}
		if record.cancel != nil {
			cancelers = append(cancelers, record.cancel)
		}
		delete(s.imageLeaseRecords, leaseID)
	}
	delete(s.imageLeases, token)
	delete(s.imageAccountRuntime, token)
	return cancelers
}

func accountLogIdentity(account Account) string {
	if email := strings.TrimSpace(account.Email); email != "" {
		return email
	}
	if id := strings.TrimSpace(account.ID); id != "" {
		return id
	}
	return accountTokenHash(account.AccessToken)
}

func compactAccountLogError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 240 {
		message = message[:240]
	}
	return message
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
		path:                       strings.TrimSpace(path),
		state:                      state,
		accounts:                   copied,
		credentialRecoveryLogs:     logs,
		imageLeases:                map[string]int{},
		imageLeaseRecords:          map[string]*imageLeaseRecord{},
		imageAccountRuntime:        map[string]*imageAccountRuntime{},
		imageMaxInflightPerAccount: 1,
		imageDynamicSlots:          true,
		imageDynamicInitialLimit:   1,
		imageLeaseChanged:          make(chan struct{}),
		now:                        time.Now,
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

func (s *Store) ImageDispatchStats() ImageDispatchStats {
	if s == nil {
		return ImageDispatchStats{}
	}
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := ImageDispatchStats{Total: len(s.accounts), MaxInflightPerAccount: NormalizeImageMaxInflightPerAccount(s.imageMaxInflightPerAccount)}
	for _, account := range s.accounts {
		stats.TotalImageSuccess += max(0, account.ImageOK)
		stats.TotalImageFailures += max(0, account.ImageFailures)
		token := strings.TrimSpace(account.AccessToken)
		status := strings.TrimSpace(account.Status)
		disabled := account.Disabled || isStatus(status, "disabled", "禁用")
		recovering := isStatus(status, StatusCredentialRecovery)
		invalid := token == "" || isStatus(status, "invalid", "token_revoked", "token_invalidated", StatusCredentialInvalid)
		abnormal := isStatus(status, "abnormal", "异常")
		limited := isStatus(status, "rate_limited", "limited", "no_quota", "限流")
		switch {
		case disabled:
			stats.Disabled++
			continue
		case recovering:
			stats.Recovering++
			continue
		case invalid:
			stats.Invalid++
			stats.Dead++
			continue
		case abnormal:
			stats.Abnormal++
			stats.Dead++
			continue
		case limited:
			stats.Limited++
			continue
		}
		if !usable(account) {
			stats.Abnormal++
			stats.Dead++
			continue
		}
		stats.Usable++
		if imageReferenceUploadCooldownUntil(account).After(now) {
			stats.ReferenceUploadCooling++
		}
		if remaining, known := imageQuotaRemaining(account); known {
			stats.KnownQuotaAccounts++
			stats.KnownRemainingQuota += remaining
			if remaining <= 0 {
				stats.QuotaExhausted++
			}
		} else {
			stats.UnknownQuotaUsable++
		}
		if imageQuotaExhausted(account) {
			continue
		}
		if until := imageCooldownUntil(account); until.After(now) {
			stats.Cooling++
			if stats.NextCooldownEndsAt == nil || until.Before(*stats.NextCooldownEndsAt) {
				copied := until
				stats.NextCooldownEndsAt = &copied
			}
			continue
		}
		stats.Dispatchable++
		leaseCount := max(0, s.imageLeases[token])
		maxInflight := s.imageEffectiveLimitLocked(token)
		stats.DynamicSlots += maxInflight
		if stats.DynamicLimitMin == 0 || maxInflight < stats.DynamicLimitMin {
			stats.DynamicLimitMin = maxInflight
		}
		if maxInflight > stats.DynamicLimitMax {
			stats.DynamicLimitMax = maxInflight
		}
		stats.DispatchableSlots += maxInflight
		stats.LeasedSlots += leaseCount
		availableSlots := max(0, maxInflight-leaseCount)
		stats.IdleSlots += availableSlots
		if leaseCount > 0 {
			stats.Leased++
		}
		if leaseCount == 0 {
			stats.Idle++
		}
		if availableSlots == 0 {
			stats.Saturated++
		}
	}
	if stats.Total > 0 {
		stats.DeadRate = round2(float64(stats.Dead) * 100 / float64(stats.Total))
		unavailable := stats.Total - stats.Dispatchable
		stats.UnavailableRate = round2(float64(unavailable) * 100 / float64(stats.Total))
		stats.DispatchableRate = round2(float64(stats.Dispatchable) * 100 / float64(stats.Total))
	}
	if stats.Usable > 0 {
		stats.CoolingRate = round2(float64(stats.Cooling) * 100 / float64(stats.Usable))
	}
	if attempts := stats.TotalImageSuccess + stats.TotalImageFailures; attempts > 0 {
		stats.HistoricalSuccessRate = round2(float64(stats.TotalImageSuccess) * 100 / float64(attempts))
		stats.HistoricalFailureRate = round2(float64(stats.TotalImageFailures) * 100 / float64(attempts))
	}
	if stats.KnownQuotaAccounts > 0 {
		stats.AverageKnownRemainingQuota = round2(float64(stats.KnownRemainingQuota) / float64(stats.KnownQuotaAccounts))
	}
	return stats
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
	cancelers := make([]context.CancelCauseFunc, 0)
	for _, account := range s.accounts {
		if wanted[account.AccessToken] {
			cancelers = append(cancelers, s.evictImageLeasesLocked(account.AccessToken)...)
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
	cancelImageLeaseContexts(cancelers, imageAccountEvictedError("account deleted"))
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

// TokensForScheduledRefresh excludes accounts that are actively generating an
// image. Background metadata checks share the account's cookie jar and should
// never race a customer request through the same browser identity.
func (s *Store) TokensForScheduledRefresh() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tokens := make([]string, 0, len(s.accounts))
	for _, account := range s.accounts {
		token := strings.TrimSpace(account.AccessToken)
		if token == "" {
			continue
		}
		if s.imageLeases[token] > 0 {
			continue
		}
		tokens = append(tokens, token)
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
	return s.SelectForImageWithRequirements(exclude, ImageDispatchRequirements{})
}

// SelectForImageWithRequirements selects an account that can satisfy the
// request's capabilities. Reference-upload cooldowns only affect requests
// that actually contain reference images.
func (s *Store) SelectForImageWithRequirements(exclude map[string]bool, requirements ImageDispatchRequirements) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.selectForImageLocked(exclude, false, requirements)
	if !ok {
		return Account{}, ErrNoAvailableAccount
	}
	return cloneAccount(account), nil
}

// TryAcquireForImage reserves an eligible account only when a slot is free at
// the instant of the call. The separate false result lets a dispatcher wait on
// another resource without holding an account lease.
func (s *Store) TryAcquireForImage(exclude map[string]bool) (Account, bool, error) {
	return s.TryAcquireForImageWithRequirements(exclude, ImageDispatchRequirements{})
}

// TryAcquireForImageWithRequirements reserves an eligible account only when
// a slot is free at the instant of the call.
func (s *Store) TryAcquireForImageWithRequirements(exclude map[string]bool, requirements ImageDispatchRequirements) (Account, bool, error) {
	if s == nil {
		return Account{}, false, ErrNoAvailableAccount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account, available := s.selectForImageLocked(exclude, true, requirements)
	if available {
		s.imageLeases[account.AccessToken]++
		return cloneAccount(account), true, nil
	}
	_, eligible := s.selectForImageLocked(exclude, false, requirements)
	_, cooling := s.earliestImageCooldownLocked(exclude, requirements)
	if !eligible && !cooling {
		return Account{}, false, ErrNoAvailableAccount
	}
	return Account{}, false, nil
}

// WaitForImageAvailability waits for an account slot, a cooldown expiry, or a
// pool change. It reserves no account and is intended for coordinated
// dispatchers that must acquire several independent resources.
func (s *Store) WaitForImageAvailability(ctx context.Context, exclude map[string]bool) error {
	return s.WaitForImageAvailabilityWithRequirements(ctx, exclude, ImageDispatchRequirements{})
}

// WaitForImageAvailabilityWithRequirements waits for an account slot, a
// matching capability cooldown expiry, or a pool change.
func (s *Store) WaitForImageAvailabilityWithRequirements(ctx context.Context, exclude map[string]bool, requirements ImageDispatchRequirements) error {
	if s == nil {
		return ErrNoAvailableAccount
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.mu.Lock()
		_, available := s.selectForImageLocked(exclude, true, requirements)
		if available {
			s.mu.Unlock()
			return nil
		}
		_, eligible := s.selectForImageLocked(exclude, false, requirements)
		cooldownUntil, cooling := s.earliestImageCooldownLocked(exclude, requirements)
		changed := s.imageLeaseChanged
		s.mu.Unlock()
		if !eligible && !cooling {
			return ErrNoAvailableAccount
		}
		if err := waitForImageAvailability(ctx, changed, cooldownUntil); err != nil {
			return err
		}
	}
}

// AcquireForImage reserves one idle account for an image request. If all
// otherwise-eligible accounts are occupied, it waits for a release so callers
// can remain in the task queue without starting a second request on a token.
func (s *Store) AcquireForImage(ctx context.Context, exclude map[string]bool, onWait func()) (Account, error) {
	return s.AcquireForImageWithRequirements(ctx, exclude, ImageDispatchRequirements{}, onWait)
}

// AcquireForImageWithRequirements reserves one account for an image request
// while honoring capability-specific cooldowns.
func (s *Store) AcquireForImageWithRequirements(ctx context.Context, exclude map[string]bool, requirements ImageDispatchRequirements, onWait func()) (Account, error) {
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
			account, available := s.selectForImageLocked(exclude, true, requirements)
			if available {
				if waiter != nil {
					s.removeImageWaiterLocked(waiter)
				}
				s.imageLeases[account.AccessToken]++
				s.mu.Unlock()
				return cloneAccount(account), nil
			}
			_, eligible := s.selectForImageLocked(exclude, false, requirements)
			cooldownUntil, cooling := s.earliestImageCooldownLocked(exclude, requirements)
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
	return s.AcquireAccountForImageWithRequirements(ctx, token, ImageDispatchRequirements{}, onWait)
}

// AcquireAccountForImageWithRequirements reserves a specific account while
// applying the same capability checks as the normal image dispatcher.
func (s *Store) AcquireAccountForImageWithRequirements(ctx context.Context, token string, requirements ImageDispatchRequirements, onWait func()) (Account, error) {
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
		if !usable(account) || isImageDispatchBlocked(account, requirements, s.now()) {
			s.mu.Unlock()
			return Account{}, ErrNoAvailableAccount
		}
		maxInflight := s.imageEffectiveLimitLocked(account.AccessToken)
		if s.imageLeases[token] < maxInflight {
			s.imageLeases[token]++
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
	var cancel context.CancelCauseFunc
	var leaseID string
	var active, limit int
	s.mu.Lock()
	for candidateID, record := range s.imageLeaseRecords {
		if record == nil || record.Token != token {
			continue
		}
		leaseID = candidateID
		cancel = record.cancel
		delete(s.imageLeaseRecords, candidateID)
		break
	}
	leaseCount := s.imageLeases[token]
	if leaseID != "" && leaseCount > 0 {
		leaseCount--
	}
	if leaseID == "" {
		if leaseCount <= 0 {
			s.mu.Unlock()
			return
		}
		leaseCount--
	}
	if leaseCount <= 0 {
		delete(s.imageLeases, token)
	} else {
		s.imageLeases[token] = leaseCount
	}
	active = max(0, leaseCount)
	limit = s.imageEffectiveLimitLocked(token)
	s.signalImageAvailabilityLocked()
	s.mu.Unlock()
	if cancel != nil {
		cancel(nil)
	}
	if leaseID != "" {
		log.Printf("image_account_lease event=released_legacy lease_id=%s account_token_hash=%s active=%d limit=%d", leaseID, accountTokenHash(token), active, limit)
	}
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

func (s *Store) selectForImageLocked(exclude map[string]bool, skipOccupied bool, requirements ImageDispatchRequirements) (Account, bool) {
	now := s.now()
	var selected Account
	selectedLeases := 0
	found := false
	for _, a := range s.accounts {
		if !usable(a) {
			continue
		}
		if isImageDispatchBlocked(a, requirements, now) {
			continue
		}
		if exclude != nil && exclude[a.AccessToken] {
			continue
		}
		leaseCount := max(0, s.imageLeases[a.AccessToken])
		maxInflight := s.imageEffectiveLimitLocked(a.AccessToken)
		if skipOccupied && leaseCount >= maxInflight {
			continue
		}
		if !found || (skipOccupied && leaseCount < selectedLeases) || ((!skipOccupied || leaseCount == selectedLeases) && imageAccountPreferred(a, selected)) {
			selected = a
			selectedLeases = leaseCount
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

func (s *Store) earliestImageCooldownLocked(exclude map[string]bool, requirements ImageDispatchRequirements) (time.Time, bool) {
	now := s.now()
	var earliest time.Time
	for _, account := range s.accounts {
		if !usable(account) || (exclude != nil && exclude[account.AccessToken]) {
			continue
		}
		until := imageDispatchCooldownUntil(account, requirements)
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

// MarkImageSuccess records an image result and updates the local remaining-
// quota estimate. A known quota reaching zero removes the account immediately.
func (s *Store) MarkImageSuccess(token string) error {
	quotaExhausted := false
	err := s.updateByToken(token, func(a *Account) {
		now := s.now()
		recordSuccess(a, now)
		if a.ImageQuotaUnknown {
			return
		}
		quotaWasKnown := imageQuotaKnown(*a)
		updateImageQuotaTotal(a, a.Quota)
		if a.Quota > 0 {
			a.Quota--
		}
		updateImageQuotaRemaining(a.Extra, a.Quota)
		a.Extra["image_quota_estimated_at"] = now.In(time.Local).Format(time.RFC3339)
		if quotaWasKnown && a.Quota == 0 {
			quotaExhausted = true
		}
	})
	if err == nil && quotaExhausted {
		_, removeErr := s.RemoveQuotaExhausted(token, errors.New("local image quota exhausted"))
		if removeErr != nil {
			return removeErr
		}
	}
	s.recordImageHealthSuccess(token)
	return err
}

func (s *Store) recordImageHealthSuccess(token string) {
	token = strings.TrimSpace(token)
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	account, found := s.accountByTokenLocked(token)
	if !found {
		s.mu.Unlock()
		return
	}
	runtime := s.imageRuntimeLocked(token)
	runtime.Successes++
	runtime.ConsecutiveSuccesses++
	runtime.ConsecutiveFailures = 0
	runtime.LastSuccessAt = s.now()
	runtime.LastReason = "success"
	if runtime.HealthScore <= 0 {
		runtime.HealthScore = 0.5
	}
	runtime.HealthScore = minFloat(1, runtime.HealthScore*0.8+0.2)
	ceiling := NormalizeImageMaxInflightPerAccount(s.imageMaxInflightPerAccount)
	oldLimit := runtime.EffectiveLimit
	if s.imageDynamicSlots && runtime.EffectiveLimit < ceiling && runtime.Successes >= 10 && runtime.ConsecutiveSuccesses >= 5 && runtime.HealthScore >= 0.8 {
		runtime.EffectiveLimit++
	}
	active := s.imageLeases[token]
	limit := s.imageEffectiveLimitLocked(token)
	identity := accountLogIdentity(account)
	health := runtime.HealthScore
	successes := runtime.Successes
	failures := runtime.Failures
	shouldLog := limit != oldLimit || successes%10 == 0
	if limit != oldLimit {
		s.signalImageAvailabilityLocked()
	}
	s.mu.Unlock()
	if shouldLog {
		log.Printf("image_account_health event=success account=%s active=%d limit=%d previous_limit=%d health=%.3f successes=%d failures=%d", identity, active, limit, oldLimit, health, successes, failures)
	}
}

func (s *Store) recordImageHealthFailure(token, reason string) {
	token = strings.TrimSpace(token)
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	account, found := s.accountByTokenLocked(token)
	if !found {
		s.mu.Unlock()
		return
	}
	runtime := s.imageRuntimeLocked(token)
	runtime.Failures++
	runtime.ConsecutiveFailures++
	runtime.ConsecutiveSuccesses = 0
	runtime.LastFailureAt = s.now()
	runtime.LastReason = strings.TrimSpace(reason)
	if runtime.HealthScore <= 0 {
		runtime.HealthScore = 0.5
	}
	runtime.HealthScore = maxFloat(0, runtime.HealthScore*0.65)
	oldLimit := runtime.EffectiveLimit
	if s.imageDynamicSlots && runtime.EffectiveLimit > 1 {
		runtime.EffectiveLimit = 1
	}
	active := s.imageLeases[token]
	limit := s.imageEffectiveLimitLocked(token)
	identity := accountLogIdentity(account)
	health := runtime.HealthScore
	successes := runtime.Successes
	failures := runtime.Failures
	if limit != oldLimit {
		s.signalImageAvailabilityLocked()
	}
	s.mu.Unlock()
	log.Printf("image_account_health event=failure account=%s active=%d limit=%d previous_limit=%d health=%.3f successes=%d failures=%d reason=%s", identity, active, limit, oldLimit, health, successes, failures, compactAccountLogError(errors.New(reason)))
}

func (s *Store) accountByTokenLocked(token string) (Account, bool) {
	for _, account := range s.accounts {
		if account.AccessToken == token {
			return account, true
		}
	}
	return Account{}, false
}

// RemoveQuotaExhausted removes an account as soon as the upstream image
// service confirms that its image quota is exhausted.
func (s *Store) RemoveQuotaExhausted(token string, err error) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	reason := strings.TrimSpace(fmt.Sprint(err))
	s.mu.Lock()
	next := s.accounts[:0]
	removed := false
	for _, account := range s.accounts {
		if account.AccessToken == token {
			removed = true
			s.appendCredentialRecoveryLogLocked(account, "warning", "account_deleted", "账号因图片额度耗尽被自动移除", reason, 0)
			continue
		}
		next = append(next, account)
	}
	s.accounts = next
	if !removed {
		s.mu.Unlock()
		return false, nil
	}
	cancelers := s.evictImageLeasesLocked(token)
	s.signalImageAvailabilityLocked()
	s.markDirtyLocked()
	snapshot, revision := s.snapshotLocked()
	s.mu.Unlock()
	cancelImageLeaseContexts(cancelers, imageAccountEvictedError("quota exhausted"))
	return true, s.persistSnapshot(snapshot, revision)
}

// RemoveExhaustedAccounts removes every account whose known image quota is
// already zero. It is used by the independent quota cleanup job; account
// refresh checks are not involved.
func (s *Store) RemoveExhaustedAccounts() (int, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	next := make([]Account, 0, len(s.accounts))
	removedTokens := make([]string, 0)
	for _, account := range s.accounts {
		if imageQuotaExhausted(account) {
			s.appendCredentialRecoveryLogLocked(account, "warning", "account_deleted", "账号因图片额度耗尽被自动移除", "scheduled quota cleanup", 0)
			removedTokens = append(removedTokens, account.AccessToken)
			continue
		}
		next = append(next, account)
	}
	if len(removedTokens) == 0 {
		s.mu.Unlock()
		return 0, nil
	}
	s.accounts = next
	var cancelers []context.CancelCauseFunc
	for _, token := range removedTokens {
		cancelers = append(cancelers, s.evictImageLeasesLocked(token)...)
	}
	s.signalImageAvailabilityLocked()
	s.markDirtyLocked()
	snapshot, revision := s.snapshotLocked()
	s.mu.Unlock()
	cancelImageLeaseContexts(cancelers, imageAccountEvictedError("quota exhausted"))
	return len(removedTokens), s.persistSnapshot(snapshot, revision)
}

// RemoveRateLimited removes an account as soon as an upstream response or an
// account refresh explicitly identifies it as rate limited. Rate-limited
// accounts are not eligible for later reuse, so retaining a cooldown record
// would only keep dead capacity in the pool.
func (s *Store) RemoveRateLimited(token string, err error) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	reason := strings.TrimSpace(fmt.Sprint(err))
	s.mu.Lock()
	next := s.accounts[:0]
	removed := false
	for _, account := range s.accounts {
		if account.AccessToken == token {
			removed = true
			s.appendCredentialRecoveryLogLocked(account, "warning", "account_deleted", "账号因限流被自动移除", reason, 0)
			continue
		}
		next = append(next, account)
	}
	s.accounts = next
	if !removed {
		s.mu.Unlock()
		return false, nil
	}
	cancelers := s.evictImageLeasesLocked(token)
	s.signalImageAvailabilityLocked()
	s.markDirtyLocked()
	snapshot, revision := s.snapshotLocked()
	s.mu.Unlock()
	cancelImageLeaseContexts(cancelers, imageAccountEvictedError("rate limited"))
	return true, s.persistSnapshot(snapshot, revision)
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

// MarkImageRateLimited removes an account from image dispatch immediately.
func (s *Store) MarkImageRateLimited(token string, retryAfter time.Duration, err error) error {
	_, removeErr := s.RemoveRateLimited(token, err)
	return removeErr
}

// MarkImageReferenceUploadRateLimited freezes only the reference-upload
// capability. Plain text-to-image requests may continue using the account.
func (s *Store) MarkImageReferenceUploadRateLimited(token string, retryAfter time.Duration, err error) error {
	return s.updateByToken(token, func(a *Account) {
		now := s.now()
		if a.Extra == nil {
			a.Extra = map[string]any{}
		}
		cooldown := defaultImageReferenceUploadCooldown
		if retryAfter > cooldown {
			cooldown = retryAfter
		}
		until := now.Add(cooldown)
		a.LastUsedAt = now.Unix()
		a.LastError = compactImageCooldownError(err)
		a.ImageFailures++
		a.Extra["last_used_at"] = now.In(time.Local).Format(time.RFC3339)
		a.Extra[imageReferenceUploadCooldownUntilKey] = until.UTC().Format(time.RFC3339Nano)
		a.Extra[imageReferenceUploadCooldownReasonKey] = "reference_upload_rate_limited"
		a.Extra[imageReferenceUploadCooldownLastErrorKey] = a.LastError
		a.Extra[imageReferenceUploadCooldownLastAtKey] = now.UTC().Format(time.RFC3339Nano)
	})
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

// MarkImageStalled temporarily backs off an account after a submitted image
// conversation stops producing image references for the configured stall
// window. The original conversation remains available for diagnostics only.
func (s *Store) MarkImageStalled(token string, err error) error {
	return s.markImageCooldown(token, ImageCooldownGenerationStalled, 0, err)
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
	updateErr := s.updateByToken(token, func(a *Account) {
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
	s.recordImageHealthFailure(token, string(reason))
	return updateErr
}

func imageCooldownDelay(reason ImageCooldownReason, retryAfter time.Duration, failures int) time.Duration {
	base, capDelay := 15*time.Second, 3*time.Minute
	switch reason {
	case ImageCooldownRateLimited:
		base, capDelay = 30*time.Second, 15*time.Minute
	case ImageCooldownGenerationTerminated:
		base, capDelay = 20*time.Second, 5*time.Minute
	case ImageCooldownGenerationStalled:
		base, capDelay = 90*time.Second, 10*time.Minute
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
	return cooldownUntilFromExtra(account.Extra, imageCooldownUntilKey)
}

func imageReferenceUploadCooldownUntil(account Account) time.Time {
	return cooldownUntilFromExtra(account.Extra, imageReferenceUploadCooldownUntilKey)
}

func cooldownUntilFromExtra(extra map[string]any, key string) time.Time {
	if extra == nil {
		return time.Time{}
	}
	value, ok := extra[key]
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

func isImageDispatchBlocked(account Account, requirements ImageDispatchRequirements, now time.Time) bool {
	if imageQuotaExhausted(account) {
		return true
	}
	if isImageCooling(account, now) {
		return true
	}
	return requirements.NeedsReferenceUpload && imageReferenceUploadCooldownUntil(account).After(now)
}

func imageQuotaKnown(account Account) bool {
	if account.ImageQuotaUnknown {
		return false
	}
	if account.Quota > 0 {
		return true
	}
	if isStatus(account.Status, "no_quota", "限流") {
		return true
	}
	for _, key := range []string{"quota", "image_quota_total", "image_quota_synced_at"} {
		if _, ok := account.Extra[key]; ok {
			return true
		}
	}
	return imageQuotaProgressPresent(account.Extra)
}

func imageQuotaExhausted(account Account) bool {
	remaining, known := imageQuotaRemaining(account)
	return known && remaining <= 0
}

func imageQuotaRemaining(account Account) (int, bool) {
	if account.ImageQuotaUnknown {
		return 0, false
	}
	if account.Quota > 0 {
		return account.Quota, true
	}
	if remaining, ok := imageQuotaProgressRemaining(account.Extra); ok {
		return max(0, remaining), true
	}
	if imageQuotaKnown(account) {
		return max(0, account.Quota), true
	}
	return 0, false
}

func imageQuotaProgressPresent(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	match := func(value any) bool {
		item, ok := value.(map[string]any)
		if !ok {
			return false
		}
		return strings.TrimSpace(fmt.Sprint(item["feature_name"])) == "image_gen"
	}
	switch progress := extra["limits_progress"].(type) {
	case []map[string]any:
		for _, item := range progress {
			if match(item) {
				return true
			}
		}
	case []any:
		for _, item := range progress {
			if match(item) {
				return true
			}
		}
	}
	return false
}

func imageQuotaProgressRemaining(extra map[string]any) (int, bool) {
	if extra == nil {
		return 0, false
	}
	read := func(value any) (int, bool) {
		item, ok := value.(map[string]any)
		if !ok || strings.TrimSpace(fmt.Sprint(item["feature_name"])) != "image_gen" {
			return 0, false
		}
		if _, exists := item["remaining"]; !exists {
			return 0, false
		}
		return asInt(item["remaining"]), true
	}
	switch progress := extra["limits_progress"].(type) {
	case []map[string]any:
		for _, item := range progress {
			if remaining, ok := read(item); ok {
				return remaining, true
			}
		}
	case []any:
		for _, item := range progress {
			if remaining, ok := read(item); ok {
				return remaining, true
			}
		}
	}
	return 0, false
}

func imageDispatchCooldownUntil(account Account, requirements ImageDispatchRequirements) time.Time {
	until := imageCooldownUntil(account)
	if requirements.NeedsReferenceUpload {
		if referenceUntil := imageReferenceUploadCooldownUntil(account); referenceUntil.After(until) {
			until = referenceUntil
		}
	}
	return until
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
	var cancelers []context.CancelCauseFunc
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
		cancelers = s.evictImageLeasesLocked(token)
		s.signalImageAvailabilityLocked()
		result := cloneAccount(*account)
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		cancelImageLeaseContexts(cancelers, imageAccountEvictedError("credential recovery started"))
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
			cancelers := s.evictImageLeasesLocked(token)
			s.signalImageAvailabilityLocked()
			s.markDirtyLocked()
			snapshot, revision := s.snapshotLocked()
			s.mu.Unlock()
			cancelImageLeaseContexts(cancelers, imageAccountEvictedError("credential recovery deleted"))
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
		cancelers := s.evictImageLeasesLocked(token)
		s.signalImageAvailabilityLocked()
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		cancelImageLeaseContexts(cancelers, imageAccountEvictedError("authentication failed"))
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

func isRateLimitedAccount(account Account) bool {
	return isStatus(account.Status, "limited", "rate_limited", "no_quota", "限流") ||
		(!account.ImageQuotaUnknown && account.Quota <= 0)
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
