package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"imagepool/internal/persistence"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"

	authPersistDebounce = 150 * time.Millisecond
)

type Identity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

func (i Identity) IsAdmin() bool { return i.Role == RoleAdmin }

type Limits struct {
	DailyRequests    int      `json:"daily_requests"`
	DailyImages      int      `json:"daily_images"`
	AllowedModels    []string `json:"allowed_models"`
	AllowedEndpoints []string `json:"allowed_endpoints"`
}

type Usage struct {
	Date     string `json:"date"`
	Requests int    `json:"requests"`
	Images   int    `json:"images"`
}

type PublicKey struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	Limits     Limits `json:"limits"`
	Usage      Usage  `json:"usage"`
}

type KeyUpdate struct {
	Name    *string
	Enabled *bool
	Key     *string
	Limits  *Limits
}

type QuotaError struct {
	Message    string
	StatusCode int
}

func (e *QuotaError) Error() string { return e.Message }

type keyRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	KeyHash    string `json:"key_hash"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	Limits     Limits `json:"limits"`
	Usage      Usage  `json:"usage"`
}

type keyFile struct {
	Keys []keyRecord `json:"keys"`
}

type Service struct {
	mu        sync.RWMutex
	persist   sync.Mutex
	legacy    *Auth
	path      string
	state     persistence.Store
	keys      []keyRecord
	now       func() time.Time
	randomKey func() (string, error)
	dirty     bool
	revision  uint64
	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	close     sync.Once
}

func NewService(adminKeys []string, path string) *Service {
	return newService(adminKeys, path, nil)
}

func NewServiceWithPersistence(adminKeys []string, state persistence.Store) *Service {
	return newService(adminKeys, "", state)
}

func newService(adminKeys []string, path string, state persistence.Store) *Service {
	s := &Service{
		legacy: New(adminKeys),
		path:   strings.TrimSpace(path),
		state:  state,
		now:    time.Now,
		randomKey: func() (string, error) {
			buf := make([]byte, 24)
			if _, err := rand.Read(buf); err != nil {
				return "", err
			}
			return "sk-" + base64.RawURLEncoding.EncodeToString(buf), nil
		},
	}
	_ = s.load()
	if s.path != "" || s.state != nil {
		s.wake = make(chan struct{}, 1)
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		go s.persistenceLoop()
	}
	return s
}

func (s *Service) AuthenticateRequest(r *http.Request) (Identity, bool) {
	if s == nil {
		return Identity{ID: "admin", Name: "Administrator", Role: RoleAdmin}, true
	}
	for _, candidate := range []string{bearerToken(r.Header.Get("Authorization")), r.Header.Get("x-api-key"), r.Header.Get("api-key")} {
		if identity, ok := s.Authenticate(candidate); ok {
			return identity, true
		}
	}
	return Identity{}, false
}

func (s *Service) Authenticate(key string) (Identity, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Identity{}, false
	}
	s.mu.Lock()
	if s.legacy != nil {
		if _, ok := s.legacy.keys[key]; ok {
			s.mu.Unlock()
			return Identity{ID: "admin", Name: "Administrator", Role: RoleAdmin}, true
		}
	}
	hash := hashKey(key)
	for i := range s.keys {
		item := &s.keys[i]
		if item.Role != RoleUser || !item.Enabled || !sameHash(item.KeyHash, hash) {
			continue
		}
		item.LastUsedAt = s.now().In(time.Local).Format(time.RFC3339)
		identity := identityFromRecord(*item)
		s.markDirtyLocked()
		s.mu.Unlock()
		s.signalPersistence()
		return identity, true
	}
	s.mu.Unlock()
	return Identity{}, false
}

func (s *Service) UpdateAdminKeys(keys []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.legacy = New(keys)
	s.mu.Unlock()
}

func (s *Service) CreateUserKey(name string) (PublicKey, string, error) {
	s.mu.Lock()
	name = s.uniqueNameLocked(name, "")
	for attempts := 0; attempts < 5; attempts++ {
		raw, err := s.randomKey()
		if err != nil {
			s.mu.Unlock()
			return PublicKey{}, "", err
		}
		if s.isAdminKey(raw) || s.hasHashLocked(hashKey(raw), "") {
			continue
		}
		now := s.now().In(time.Local).Format(time.RFC3339)
		item := keyRecord{ID: randomID(), Name: name, Role: RoleUser, KeyHash: hashKey(raw), Enabled: true, CreatedAt: now, Limits: normalizeLimits(Limits{}), Usage: Usage{Date: dateKey(s.now()), Requests: 0, Images: 0}}
		s.keys = append(s.keys, item)
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		if err := s.persistSnapshot(snapshot, revision); err != nil {
			return PublicKey{}, "", err
		}
		return publicFromRecord(item), raw, nil
	}
	s.mu.Unlock()
	return PublicKey{}, "", fmt.Errorf("unable to generate a unique user key")
}

func (s *Service) ListUserKeys() []PublicKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]PublicKey, 0, len(s.keys))
	for _, item := range s.keys {
		if item.Role == RoleUser {
			items = append(items, publicFromRecord(item))
		}
	}
	return items
}

func (s *Service) UpdateUserKey(id string, update KeyUpdate) (PublicKey, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return PublicKey{}, false, nil
	}
	s.mu.Lock()
	for i := range s.keys {
		item := &s.keys[i]
		if item.ID != id || item.Role != RoleUser {
			continue
		}
		if update.Name != nil {
			item.Name = s.uniqueNameLocked(*update.Name, item.ID)
		}
		if update.Enabled != nil {
			item.Enabled = *update.Enabled
		}
		if update.Key != nil {
			raw := strings.TrimSpace(*update.Key)
			if raw == "" {
				s.mu.Unlock()
				return PublicKey{}, false, fmt.Errorf("key is required")
			}
			if s.isAdminKey(raw) || s.hasHashLocked(hashKey(raw), item.ID) {
				s.mu.Unlock()
				return PublicKey{}, false, fmt.Errorf("key already exists")
			}
			item.KeyHash = hashKey(raw)
		}
		if update.Limits != nil {
			item.Limits = normalizeLimits(*update.Limits)
		}
		result := publicFromRecord(*item)
		s.markDirtyLocked()
		snapshot, revision := s.snapshotLocked()
		s.mu.Unlock()
		if err := s.persistSnapshot(snapshot, revision); err != nil {
			return PublicKey{}, false, err
		}
		return result, true, nil
	}
	s.mu.Unlock()
	return PublicKey{}, false, nil
}

func (s *Service) DeleteUserKey(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	s.mu.Lock()
	next := s.keys[:0]
	removed := false
	for _, item := range s.keys {
		if item.ID == id && item.Role == RoleUser {
			removed = true
			continue
		}
		next = append(next, item)
	}
	s.keys = next
	if !removed {
		s.mu.Unlock()
		return false, nil
	}
	s.markDirtyLocked()
	snapshot, revision := s.snapshotLocked()
	s.mu.Unlock()
	return true, s.persistSnapshot(snapshot, revision)
}

func (s *Service) Consume(identity Identity, endpoint, model string, requestUnits, imageUnits int) error {
	if identity.Role != RoleUser {
		return nil
	}
	endpoint = strings.TrimSpace(endpoint)
	model = strings.TrimSpace(model)
	requestUnits = max(0, requestUnits)
	imageUnits = max(0, imageUnits)
	s.mu.Lock()
	for i := range s.keys {
		item := &s.keys[i]
		if item.ID != identity.ID || item.Role != RoleUser {
			continue
		}
		if !item.Enabled {
			s.mu.Unlock()
			return &QuotaError{Message: "user key is disabled", StatusCode: http.StatusUnauthorized}
		}
		limits := normalizeLimits(item.Limits)
		if len(limits.AllowedEndpoints) > 0 && !contains(limits.AllowedEndpoints, endpoint) {
			s.mu.Unlock()
			return &QuotaError{Message: "user key is not allowed to access this endpoint", StatusCode: http.StatusForbidden}
		}
		if len(limits.AllowedModels) > 0 && model != "" && !contains(limits.AllowedModels, model) {
			s.mu.Unlock()
			return &QuotaError{Message: "user key is not allowed to use this model", StatusCode: http.StatusForbidden}
		}
		usage := normalizeUsage(item.Usage, s.now())
		nextRequests := usage.Requests + requestUnits
		nextImages := usage.Images + imageUnits
		if limits.DailyRequests > 0 && nextRequests > limits.DailyRequests {
			s.mu.Unlock()
			return &QuotaError{Message: "daily request quota exhausted", StatusCode: http.StatusTooManyRequests}
		}
		if limits.DailyImages > 0 && nextImages > limits.DailyImages {
			s.mu.Unlock()
			return &QuotaError{Message: "daily image quota exhausted", StatusCode: http.StatusTooManyRequests}
		}
		item.Usage = Usage{Date: usage.Date, Requests: nextRequests, Images: nextImages}
		s.markDirtyLocked()
		s.mu.Unlock()
		s.signalPersistence()
		return nil
	}
	s.mu.Unlock()
	return &QuotaError{Message: "user key no longer exists", StatusCode: http.StatusUnauthorized}
}

func (s *Service) load() error {
	if s.state != nil {
		var shaped keyFile
		if err := s.state.Load(context.Background(), "auth_keys", &shaped); err != nil {
			if errors.Is(err, persistence.ErrNotFound) {
				return nil
			}
			return err
		}
		for i := range shaped.Keys {
			shaped.Keys[i] = normalizeRecord(shaped.Keys[i], s.now())
		}
		s.keys = shaped.Keys
		return nil
	}
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var shaped keyFile
	if err := json.Unmarshal(data, &shaped); err != nil {
		var list []keyRecord
		if listErr := json.Unmarshal(data, &list); listErr != nil {
			return err
		}
		shaped.Keys = list
	}
	for i := range shaped.Keys {
		shaped.Keys[i] = normalizeRecord(shaped.Keys[i], s.now())
	}
	s.keys = shaped.Keys
	return nil
}

func (s *Service) markDirtyLocked() {
	s.revision++
	s.dirty = true
}

func (s *Service) snapshotLocked() (keyFile, uint64) {
	keys := make([]keyRecord, len(s.keys))
	for i := range s.keys {
		keys[i] = cloneRecord(s.keys[i])
	}
	return keyFile{Keys: keys}, s.revision
}

func cloneRecord(item keyRecord) keyRecord {
	item.Limits.AllowedModels = append([]string(nil), item.Limits.AllowedModels...)
	item.Limits.AllowedEndpoints = append([]string(nil), item.Limits.AllowedEndpoints...)
	return item
}

func (s *Service) signalPersistence() {
	if s == nil || s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) persistenceLoop() {
	defer close(s.done)
	for {
		select {
		case <-s.wake:
			timer := time.NewTimer(authPersistDebounce)
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

// Flush persists all updates that were accepted in memory before it returns.
// Requests normally use the debounce worker so storage latency never holds the
// auth lock; Flush is for tests and graceful shutdown.
func (s *Service) Flush() error {
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

// Close stops the auth persistence worker after flushing accepted usage and
// last-used updates. The shared persistence.Store remains owned by the app.
func (s *Service) Close() {
	if s == nil || s.stop == nil {
		return
	}
	s.close.Do(func() {
		close(s.stop)
		<-s.done
		_ = s.Flush()
	})
}

func (s *Service) persistPending() error {
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

func (s *Service) persistSnapshot(snapshot keyFile, revision uint64) error {
	s.persist.Lock()
	defer s.persist.Unlock()
	return s.saveSnapshot(snapshot, revision)
}

// saveSnapshot is called with s.persist held and never holds s.mu while I/O
// runs. A revision check keeps a concurrent request update dirty for the next
// batched write instead of acknowledging an older snapshot.
func (s *Service) saveSnapshot(snapshot keyFile, revision uint64) error {
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

func (s *Service) save(shaped keyFile) error {
	if s.state != nil {
		return s.state.Save(context.Background(), "auth_keys", shaped)
	}
	if s.path == "" {
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

func (s *Service) uniqueNameLocked(name, excludeID string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "User"
	}
	base := name
	for suffix := 1; ; suffix++ {
		candidate := name
		if suffix > 1 {
			candidate = fmt.Sprintf("%s %d", base, suffix)
		}
		inUse := false
		for _, item := range s.keys {
			if item.Role == RoleUser && item.ID != excludeID && item.Name == candidate {
				inUse = true
				break
			}
		}
		if !inUse {
			return candidate
		}
	}
}

func (s *Service) isAdminKey(key string) bool {
	if s.legacy == nil {
		return false
	}
	_, ok := s.legacy.keys[key]
	return ok
}

func (s *Service) hasHashLocked(hash, excludeID string) bool {
	for _, item := range s.keys {
		if item.ID != excludeID && sameHash(item.KeyHash, hash) {
			return true
		}
	}
	return false
}

func hashKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

func sameHash(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func identityFromRecord(item keyRecord) Identity {
	return Identity{ID: item.ID, Name: item.Name, Role: item.Role}
}

func publicFromRecord(item keyRecord) PublicKey {
	return PublicKey{ID: item.ID, Name: item.Name, Role: item.Role, Enabled: item.Enabled, CreatedAt: item.CreatedAt, LastUsedAt: item.LastUsedAt, Limits: normalizeLimits(item.Limits), Usage: normalizeUsage(item.Usage, time.Now())}
}

func normalizeRecord(item keyRecord, now time.Time) keyRecord {
	if item.ID == "" {
		item.ID = randomID()
	}
	if item.Name == "" {
		item.Name = "User"
	}
	item.Role = RoleUser
	if item.CreatedAt == "" {
		item.CreatedAt = now.In(time.Local).Format(time.RFC3339)
	}
	item.Limits = normalizeLimits(item.Limits)
	item.Usage = normalizeUsage(item.Usage, now)
	return item
}

func normalizeLimits(value Limits) Limits {
	value.DailyRequests = max(0, value.DailyRequests)
	value.DailyImages = max(0, value.DailyImages)
	value.AllowedModels = normalizedStrings(value.AllowedModels)
	value.AllowedEndpoints = normalizedStrings(value.AllowedEndpoints)
	return value
}

func normalizeUsage(value Usage, now time.Time) Usage {
	if value.Date != dateKey(now) {
		return Usage{Date: dateKey(now)}
	}
	value.Requests = max(0, value.Requests)
	value.Images = max(0, value.Images)
	return value
}

func normalizedStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func dateKey(now time.Time) string { return now.In(time.Local).Format("2006-01-02") }

func randomID() string {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("key-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type identityContextKey struct{}

func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, identity)
}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(Identity)
	return identity, ok
}
