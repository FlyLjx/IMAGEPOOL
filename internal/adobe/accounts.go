package adobe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	HTTPOnly bool   `json:"http_only"`
	Secure   bool   `json:"secure"`
	SameSite string `json:"same_site"`
}

type Account struct {
	AccountID             string       `json:"account_id"`
	ProfileID             string       `json:"-"`
	State                 string       `json:"state"`
	RegistrationID        string       `json:"-"`
	Source                string       `json:"source"`
	Refreshable           bool         `json:"refreshable"`
	CapturedAt            time.Time    `json:"captured_at"`
	Email                 string       `json:"email"`
	DisplayName           string       `json:"display_name"`
	TokenExpiresAt        *time.Time   `json:"token_expires_at,omitempty"`
	ClientContext         ClientPolicy `json:"-"`
	RouteAffinity         string       `json:"route_affinity"`
	SessionVersion        int64        `json:"-"`
	LastVerifiedAt        *time.Time   `json:"last_verified_at,omitempty"`
	LastUsedAt            *time.Time   `json:"last_used_at,omitempty"`
	ConsecutiveFailures   int          `json:"consecutive_failures"`
	CooldownUntil         *time.Time   `json:"cooldown_until,omitempty"`
	LastErrorCode         string       `json:"last_error_code,omitempty"`
	LastError             string       `json:"last_error,omitempty"`
	CreditsTotal          *float64     `json:"credits_total,omitempty"`
	CreditsUsed           *float64     `json:"credits_used,omitempty"`
	CreditsAvailable      *float64     `json:"credits_available,omitempty"`
	CreditsAvailableUntil string       `json:"credits_available_until,omitempty"`
	CreditsUpdatedAt      *time.Time   `json:"credits_updated_at,omitempty"`
	CreditsError          string       `json:"credits_error,omitempty"`
	RouteVersion          int64        `json:"-"`
	Disabled              bool         `json:"disabled"`
	CreatedAt             time.Time    `json:"created_at"`
	UpdatedAt             time.Time    `json:"updated_at"`
}

type AccountSession struct {
	Account     Account
	AccessToken string
	CookieJar   []Cookie
}

type SessionSnapshot struct {
	AccountID      string
	AccessToken    string
	UserAgent      string
	SecCHUA        string
	AcceptLanguage string
	Adobe2API      bool
}

func (session AccountSession) Snapshot() SessionSnapshot {
	return SessionSnapshot{
		AccountID: session.Account.AccountID, AccessToken: session.AccessToken,
		UserAgent: session.Account.ClientContext.UserAgent, SecCHUA: session.Account.ClientContext.SecCHUA,
		AcceptLanguage: session.Account.ClientContext.AcceptLanguage, Adobe2API: true,
	}
}

func AccountIDFromToken(token string) (string, error) {
	parts := strings.Split(cleanBearerToken(token), ".")
	if len(parts) < 2 {
		return "", errors.New("access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	for _, key := range []string{"user_id", "aa_id", "sub"} {
		if value := strings.TrimSpace(fmt.Sprint(claims[key])); value != "" && value != "<nil>" {
			return value, nil
		}
	}
	return "", errors.New("access token has no Adobe account identifier")
}

func TokenExpiresAt(token string) *time.Time {
	parts := strings.Split(cleanBearerToken(token), ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	seconds, _ := claims["exp"].(float64)
	if seconds <= 0 {
		createdAt, _ := claims["created_at"].(float64)
		expiresIn, _ := claims["expires_in"].(float64)
		if createdAt > 0 && expiresIn > 0 {
			seconds = createdAt + expiresIn
		}
	}
	if seconds <= 0 {
		return nil
	}
	expiresAt := time.Unix(int64(seconds), 0).UTC()
	return &expiresAt
}

func (r *Repository) GetAccount(ctx context.Context, accountID string) (Account, error) {
	var account Account
	err := scanAccount(r.db.QueryRow(ctx, accountSelectSQL+` WHERE account_id=$1`, strings.TrimSpace(accountID)), &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return account, err
}

func (r *Repository) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := r.db.Query(ctx, accountSelectSQL+` ORDER BY disabled,state,COALESCE(last_used_at,'epoch'),created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]Account, 0)
	for rows.Next() {
		var account Account
		if err := scanAccount(rows, &account); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (r *Repository) SetAccountDisabled(ctx context.Context, accountID string, disabled bool) (Account, error) {
	state := "ready"
	if disabled {
		state = "disabled"
	}
	var account Account
	err := scanAccount(r.db.QueryRow(ctx, `
UPDATE adobe_accounts SET disabled=$2,state=CASE WHEN $2 THEN 'disabled' WHEN state='disabled' THEN $3 ELSE state END,updated_at=NOW()
WHERE account_id=$1
RETURNING `+accountReturningSQL, strings.TrimSpace(accountID), disabled, state), &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return account, err
}

func (r *Repository) DeleteAccount(ctx context.Context, accountID string) error {
	result, err := r.db.Exec(ctx, `DELETE FROM adobe_accounts WHERE account_id=$1`, strings.TrimSpace(accountID))
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) AccountSession(ctx context.Context, accountID string) (AccountSession, error) {
	return r.readAccountSession(r.db.QueryRow(ctx, accountSelectSQLWithSecrets+` WHERE account_id=$1`, strings.TrimSpace(accountID)))
}

// SelectAccountSession performs a short, atomic round-robin selection. It does
// not reserve the account or create a persistent generation record.
func (r *Repository) SelectAccountSession(ctx context.Context, excluded []string, preferred string) (AccountSession, error) {
	row := r.db.QueryRow(ctx, `
WITH candidate AS (
 SELECT account.account_id
 FROM adobe_accounts account
 JOIN adobe_routes route ON route.id=account.route_id
 WHERE account.state='ready' AND account.disabled=FALSE
   AND (account.cooldown_until IS NULL OR account.cooldown_until<=NOW())
   AND (account.token_expires_at IS NULL OR account.token_expires_at>NOW()+INTERVAL '1 minute')
   AND route.enabled=TRUE AND route.health_status IN ('unknown','healthy')
   AND (route.cooldown_until IS NULL OR route.cooldown_until<=NOW())
   AND (COALESCE(array_length($1::text[],1),0)=0 OR NOT account.account_id=ANY($1::text[]))
   AND ($2='' OR account.account_id=$2)
 ORDER BY account.last_used_at NULLS FIRST,account.consecutive_failures,account.created_at DESC
 FOR UPDATE OF account SKIP LOCKED LIMIT 1
)
UPDATE adobe_accounts account SET last_used_at=NOW(),updated_at=NOW()
WHERE account.account_id=(SELECT candidate.account_id FROM candidate)
RETURNING `+accountReturningSQLWithSecrets, excluded, strings.TrimSpace(preferred))
	session, err := r.readAccountSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountSession{}, ErrNoAvailableAccount
	}
	return session, err
}

func (r *Repository) readAccountSession(row rowScanner) (AccountSession, error) {
	var session AccountSession
	var cookiesEncrypted, tokenEncrypted string
	if err := scanAccountWithSecrets(row, &session.Account, &cookiesEncrypted, &tokenEncrypted); err != nil {
		return AccountSession{}, err
	}
	cookiesJSON, err := r.cipher.Decrypt(cookiesEncrypted, SecretAAD("adobe_accounts", session.Account.AccountID, "cookie_jar"))
	if err != nil {
		return AccountSession{}, err
	}
	if err := json.Unmarshal(cookiesJSON, &session.CookieJar); err != nil {
		return AccountSession{}, err
	}
	token, err := r.cipher.Decrypt(tokenEncrypted, SecretAAD("adobe_accounts", session.Account.AccountID, "access_token"))
	if err != nil {
		return AccountSession{}, err
	}
	session.AccessToken = string(token)
	return session, nil
}

func (r *Repository) RecordGenerationResult(ctx context.Context, accountID string, generationErr error) error {
	state, code := "ready", ""
	resetFailures, incrementFailure := generationErr == nil, false
	if generationErr != nil {
		var upstream *upstreamError
		if errors.As(generationErr, &upstream) {
			code = upstream.Kind
			switch upstream.Kind {
			case "auth_invalid":
				state = "reauth_required"
			case "quota_exhausted":
				state = "exhausted"
			case "upstream_temporary", "network", "timeout":
				incrementFailure = true
			}
		}
	}
	_, err := r.db.Exec(ctx, `
UPDATE adobe_accounts SET
 state=CASE WHEN disabled THEN 'disabled' WHEN $2<>'' THEN $2 ELSE state END,
 consecutive_failures=CASE WHEN $3 THEN 0 WHEN $4 THEN consecutive_failures+1 ELSE consecutive_failures END,
 cooldown_until=CASE WHEN $3 THEN NULL WHEN $4 AND consecutive_failures+1>=3 THEN NOW()+INTERVAL '5 minutes' ELSE cooldown_until END,
 last_error_code=$5,last_error=$6,last_verified_at=CASE WHEN $3 THEN NOW() ELSE last_verified_at END,updated_at=NOW()
WHERE account_id=$1`, strings.TrimSpace(accountID), state, resetFailures, incrementFailure, code, truncateError(generationErr))
	return err
}

func (r *Repository) RecordAccountAuthFailure(ctx context.Context, accountID string, authErr error) (Account, error) {
	var account Account
	err := scanAccount(r.db.QueryRow(ctx, `
UPDATE adobe_accounts SET
 state=CASE WHEN disabled THEN 'disabled' ELSE 'reauth_required' END,
 last_error_code='auth_invalid',last_error=$2,updated_at=NOW()
WHERE account_id=$1 RETURNING `+accountReturningSQL,
		strings.TrimSpace(accountID), truncateError(authErr)), &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return account, err
}

func (r *Repository) WithAccountLock(ctx context.Context, accountID string, fn func(context.Context) error) error {
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	key := strings.TrimSpace(accountID)
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, key); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key)
	return fn(ctx)
}

const accountReturningSQL = `account_id,profile_id,state,registration_id,captured_at,email,display_name,token_expires_at,client_context,COALESCE(route_id,''),session_version,last_verified_at,last_used_at,consecutive_failures,cooldown_until,last_error_code,last_error,credits_total,credits_used,credits_available,credits_available_until,credits_updated_at,credits_error,route_version,disabled,created_at,updated_at`
const accountReturningSQLWithSecrets = accountReturningSQL + `,cookie_jar_encrypted,access_token_encrypted`
const accountSelectSQL = `SELECT ` + accountReturningSQL + ` FROM adobe_accounts`
const accountSelectSQLWithSecrets = `SELECT ` + accountReturningSQLWithSecrets + ` FROM adobe_accounts`

type rowScanner interface {
	Scan(...any) error
}

func scanAccount(row rowScanner, account *Account) error {
	var clientJSON []byte
	err := row.Scan(
		&account.AccountID, &account.ProfileID, &account.State, &account.RegistrationID, &account.CapturedAt,
		&account.Email, &account.DisplayName, &account.TokenExpiresAt, &clientJSON, &account.RouteAffinity,
		&account.SessionVersion, &account.LastVerifiedAt, &account.LastUsedAt, &account.ConsecutiveFailures,
		&account.CooldownUntil, &account.LastErrorCode, &account.LastError,
		&account.CreditsTotal, &account.CreditsUsed, &account.CreditsAvailable, &account.CreditsAvailableUntil,
		&account.CreditsUpdatedAt, &account.CreditsError,
		&account.RouteVersion, &account.Disabled, &account.CreatedAt, &account.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(clientJSON, &account.ClientContext); err != nil {
		return err
	}
	setAccountImportMetadata(account)
	return nil
}

func scanAccountWithSecrets(row rowScanner, account *Account, cookiesEncrypted, tokenEncrypted *string) error {
	var clientJSON []byte
	err := row.Scan(
		&account.AccountID, &account.ProfileID, &account.State, &account.RegistrationID, &account.CapturedAt,
		&account.Email, &account.DisplayName, &account.TokenExpiresAt, &clientJSON, &account.RouteAffinity,
		&account.SessionVersion, &account.LastVerifiedAt, &account.LastUsedAt, &account.ConsecutiveFailures,
		&account.CooldownUntil, &account.LastErrorCode, &account.LastError,
		&account.CreditsTotal, &account.CreditsUsed, &account.CreditsAvailable, &account.CreditsAvailableUntil,
		&account.CreditsUpdatedAt, &account.CreditsError, &account.RouteVersion,
		&account.Disabled, &account.CreatedAt, &account.UpdatedAt, cookiesEncrypted, tokenEncrypted,
	)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(clientJSON, &account.ClientContext); err != nil {
		return err
	}
	setAccountImportMetadata(account)
	return nil
}

func setAccountImportMetadata(account *Account) {
	account.Source = "token"
	if strings.HasPrefix(account.RegistrationID, "adobe2api:cookie:") {
		account.Source = "cookie"
		account.Refreshable = true
	}
}
