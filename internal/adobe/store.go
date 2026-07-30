package adobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound           = errors.New("Adobe record not found")
	ErrNoAvailableAccount = errors.New("no Adobe account is currently available")
	ErrNoAvailableRoute   = errors.New("no healthy Adobe route is available")
	ErrRouteInUse         = errors.New("Adobe route is assigned to one or more accounts")
)

type Repository struct {
	db     *pgxpool.Pool
	cipher *Cipher
}

type Route struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Kind                string     `json:"kind"`
	Region              string     `json:"region"`
	Priority            int        `json:"priority"`
	Enabled             bool       `json:"enabled"`
	HealthStatus        string     `json:"health_status"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	CooldownUntil       *time.Time `json:"cooldown_until,omitempty"`
	LastCheckedAt       *time.Time `json:"last_checked_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type RouteInput struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	ProxyURL string `json:"proxy_url"`
	Region   string `json:"region"`
	Priority int    `json:"priority"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

type ClientPolicy struct {
	TLSProfile            string `json:"tls_profile"`
	UserAgent             string `json:"user_agent"`
	SecCHUA               string `json:"sec_ch_ua"`
	AcceptLanguage        string `json:"accept_language"`
	RefreshUserAgent      string `json:"refresh_user_agent"`
	RefreshAcceptLanguage string `json:"refresh_accept_language"`
}

func NewRepository(db *pgxpool.Pool, cipher *Cipher) (*Repository, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL is required for Adobe")
	}
	if cipher == nil {
		return nil, errors.New("Adobe cipher is required")
	}
	return &Repository{db: db, cipher: cipher}, nil
}

func (r *Repository) CreateRoute(ctx context.Context, input RouteInput) (Route, error) {
	rawRoute := input.ProxyURL
	if strings.EqualFold(strings.TrimSpace(input.Kind), "direct") {
		rawRoute = directRouteURL
	}
	proxyURL, kind, err := normalizeRouteURL(rawRoute)
	if err != nil {
		return Route{}, err
	}
	id, err := randomID("route_adobe_", 12)
	if err != nil {
		return Route{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = id
	}
	region := strings.TrimSpace(input.Region)
	if region == "" {
		region = "auto"
	}
	priority := input.Priority
	if priority <= 0 {
		priority = 100
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	encrypted, err := r.cipher.Encrypt([]byte(proxyURL), SecretAAD("adobe_routes", id, "proxy_url"))
	if err != nil {
		return Route{}, err
	}
	var route Route
	err = r.db.QueryRow(ctx, `
INSERT INTO adobe_routes(id,name,proxy_url_encrypted,kind,region,priority,enabled)
VALUES($1,$2,$3,$4,$5,$6,$7)
RETURNING id,name,kind,region,priority,enabled,health_status,consecutive_failures,cooldown_until,last_checked_at,last_error,created_at,updated_at`,
		id, name, encrypted, kind, region, priority, enabled).Scan(routeScanTargets(&route)...)
	return route, err
}

func (r *Repository) ListRoutes(ctx context.Context) ([]Route, error) {
	rows, err := r.db.Query(ctx, `
SELECT id,name,kind,region,priority,enabled,health_status,consecutive_failures,cooldown_until,last_checked_at,last_error,created_at,updated_at
FROM adobe_routes ORDER BY priority,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	routes := make([]Route, 0)
	for rows.Next() {
		var route Route
		if err := rows.Scan(routeScanTargets(&route)...); err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func (r *Repository) GetRoute(ctx context.Context, routeID string) (Route, error) {
	var route Route
	err := r.db.QueryRow(ctx, `
SELECT id,name,kind,region,priority,enabled,health_status,consecutive_failures,cooldown_until,last_checked_at,last_error,created_at,updated_at
FROM adobe_routes WHERE id=$1`, strings.TrimSpace(routeID)).Scan(routeScanTargets(&route)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, ErrNotFound
	}
	return route, err
}

func (r *Repository) SetRouteEnabled(ctx context.Context, routeID string, enabled bool) (Route, error) {
	var route Route
	err := r.db.QueryRow(ctx, `
UPDATE adobe_routes SET enabled=$2,updated_at=NOW() WHERE id=$1
RETURNING id,name,kind,region,priority,enabled,health_status,consecutive_failures,cooldown_until,last_checked_at,last_error,created_at,updated_at`, strings.TrimSpace(routeID), enabled).Scan(routeScanTargets(&route)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, ErrNotFound
	}
	return route, err
}

func (r *Repository) ReassignAccountsFromRoute(ctx context.Context, failedRouteID string) ([]Account, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT account_id FROM adobe_accounts WHERE route_id=$1 AND disabled=FALSE FOR UPDATE`, strings.TrimSpace(failedRouteID))
	if err != nil {
		return nil, err
	}
	accountIDs := make([]string, 0)
	for rows.Next() {
		var accountID string
		if err := rows.Scan(&accountID); err != nil {
			rows.Close()
			return nil, err
		}
		accountIDs = append(accountIDs, accountID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	updated := make([]Account, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		var replacement string
		err := tx.QueryRow(ctx, `
SELECT route.id FROM adobe_routes route
WHERE route.id<>$1 AND route.enabled=TRUE AND route.health_status='healthy'
  AND (route.cooldown_until IS NULL OR route.cooldown_until<=NOW())
ORDER BY route.priority,(SELECT COUNT(*) FROM adobe_accounts account WHERE account.route_id=route.id),route.id
FOR UPDATE OF route SKIP LOCKED LIMIT 1`, strings.TrimSpace(failedRouteID)).Scan(&replacement)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, updateErr := tx.Exec(ctx, `
UPDATE adobe_accounts SET
 last_error_code=CASE WHEN state='ready' THEN 'route_unavailable' ELSE last_error_code END,
 last_error=CASE WHEN state='ready' THEN 'assigned route is unavailable' ELSE last_error END,
 updated_at=NOW()
WHERE account_id=$1`, accountID); updateErr != nil {
				return nil, updateErr
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `
UPDATE adobe_accounts SET route_id=$2,route_version=route_version+1,
 last_error_code=CASE WHEN state='ready' AND last_error_code='route_unavailable' THEN '' ELSE last_error_code END,
 last_error=CASE WHEN state='ready' AND last_error_code='route_unavailable' THEN '' ELSE last_error END,
 updated_at=NOW()
WHERE account_id=$1`, accountID, replacement)
		if err != nil {
			return nil, err
		}
		var account Account
		if err := scanAccount(tx.QueryRow(ctx, accountSelectSQL+` WHERE account_id=$1`, accountID), &account); err != nil {
			return nil, err
		}
		updated = append(updated, account)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}

func (r *Repository) RouteProxyURL(ctx context.Context, routeID string) (string, error) {
	var encrypted string
	if err := r.db.QueryRow(ctx, `SELECT proxy_url_encrypted FROM adobe_routes WHERE id=$1`, strings.TrimSpace(routeID)).Scan(&encrypted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	plain, err := r.cipher.Decrypt(encrypted, SecretAAD("adobe_routes", strings.TrimSpace(routeID), "proxy_url"))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (r *Repository) DeleteRoute(ctx context.Context, routeID string) error {
	routeID = strings.TrimSpace(routeID)
	result, err := r.db.Exec(ctx, `
DELETE FROM adobe_routes route
WHERE route.id=$1 AND NOT EXISTS (SELECT 1 FROM adobe_accounts account WHERE account.route_id=route.id)`, routeID)
	if err != nil {
		return err
	}
	if result.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM adobe_routes WHERE id=$1)`, routeID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrRouteInUse
	}
	return ErrNotFound
}

func (r *Repository) RecordRouteHealth(ctx context.Context, routeID string, checkErr error, threshold, cooldownSeconds int) error {
	if threshold <= 0 {
		threshold = 3
	}
	if cooldownSeconds <= 0 {
		cooldownSeconds = 300
	}
	if checkErr == nil {
		_, err := r.db.Exec(ctx, `UPDATE adobe_routes SET health_status='healthy',consecutive_failures=0,cooldown_until=NULL,last_checked_at=NOW(),last_error='',updated_at=NOW() WHERE id=$1`, strings.TrimSpace(routeID))
		return err
	}
	_, err := r.db.Exec(ctx, `
UPDATE adobe_routes
SET consecutive_failures=consecutive_failures+1,
    health_status=CASE WHEN consecutive_failures+1 >= $2 THEN 'unhealthy' ELSE health_status END,
    cooldown_until=CASE WHEN consecutive_failures+1 >= $2 THEN NOW()+make_interval(secs => $3) ELSE cooldown_until END,
    last_checked_at=NOW(),last_error=$4,updated_at=NOW()
WHERE id=$1`, strings.TrimSpace(routeID), threshold, cooldownSeconds, truncateError(checkErr))
	return err
}

func routeScanTargets(route *Route) []any {
	return []any{&route.ID, &route.Name, &route.Kind, &route.Region, &route.Priority, &route.Enabled, &route.HealthStatus, &route.ConsecutiveFailures, &route.CooldownUntil, &route.LastCheckedAt, &route.LastError, &route.CreatedAt, &route.UpdatedAt}
}

const directRouteURL = "direct://"

func IsDirectRouteURL(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return raw == "direct" || raw == directRouteURL
}

func normalizeRouteURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if IsDirectRouteURL(raw) {
		return directRouteURL, "direct", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", "", errors.New("proxy_url must be an absolute proxy URL or direct://")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", "", fmt.Errorf("unsupported Adobe proxy scheme %q", parsed.Scheme)
	}
	return parsed.String(), "proxy", nil
}

func normalizeClientPolicy(policy ClientPolicy) ClientPolicy {
	if strings.TrimSpace(policy.TLSProfile) == "" {
		policy.TLSProfile = "chrome146"
	}
	if strings.TrimSpace(policy.UserAgent) == "" {
		policy.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	}
	if strings.TrimSpace(policy.SecCHUA) == "" {
		policy.SecCHUA = `"Chromium";v="146", "Google Chrome";v="146", "Not_A Brand";v="99"`
	}
	if strings.TrimSpace(policy.AcceptLanguage) == "" {
		policy.AcceptLanguage = "zh-CN,zh;q=0.9"
	}
	if strings.TrimSpace(policy.RefreshUserAgent) == "" {
		policy.RefreshUserAgent = "Mozilla/5.0"
	}
	if strings.TrimSpace(policy.RefreshAcceptLanguage) == "" {
		policy.RefreshAcceptLanguage = "zh-CN,zh;q=0.9"
	}
	return policy
}

func randomID(prefix string, byteCount int) (string, error) {
	data := make([]byte, byteCount)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(data), nil
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}
