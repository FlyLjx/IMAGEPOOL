package adobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	adobeCreditsURL    = "https://firefly.adobe.io/v1/credits/balance"
	adobeCreditsAPIKey = "SunbreakWebUI1"
)

type AccountCredits struct {
	Total          *float64
	Used           *float64
	Available      *float64
	AvailableUntil string
	UpdatedAt      time.Time
}

type adobeCreditsHTTPError struct {
	StatusCode int
	Message    string
}

func (e *adobeCreditsHTTPError) Error() string {
	if strings.TrimSpace(e.Message) != "" {
		return fmt.Sprintf("Adobe credits returned HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("Adobe credits returned HTTP %d", e.StatusCode)
}

func isAdobeCreditsAuthError(err error) bool {
	var responseErr *adobeCreditsHTTPError
	return errors.As(err, &responseErr) && (responseErr.StatusCode == http.StatusUnauthorized || responseErr.StatusCode == http.StatusForbidden)
}

func fetchAdobeCredits(ctx context.Context, client *http.Client, session SessionSnapshot) (AccountCredits, error) {
	if strings.TrimSpace(session.AccessToken) == "" || strings.TrimSpace(session.AccountID) == "" {
		return AccountCredits{}, errors.New("Adobe access token and account id are required for credits")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, adobeCreditsURL, nil)
	if err != nil {
		return AccountCredits{}, err
	}
	request.Header.Set("Authorization", "Bearer "+cleanBearerToken(session.AccessToken))
	request.Header.Set("x-api-key", adobeCreditsAPIKey)
	request.Header.Set("x-account-id", session.AccountID)
	request.Header.Set("Origin", "https://new.express.adobe.com")
	request.Header.Set("Referer", "https://new.express.adobe.com/")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(session.UserAgent) != "" {
		request.Header.Set("User-Agent", session.UserAgent)
	}
	if strings.TrimSpace(session.AcceptLanguage) != "" {
		request.Header.Set("Accept-Language", session.AcceptLanguage)
	}

	response, err := client.Do(request)
	if err != nil {
		return AccountCredits{}, fmt.Errorf("Adobe credits request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return AccountCredits{}, fmt.Errorf("read Adobe credits response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return AccountCredits{}, &adobeCreditsHTTPError{StatusCode: response.StatusCode, Message: adobeRefreshErrorMessage(body)}
	}
	return decodeAdobeCredits(body)
}

func decodeAdobeCredits(body []byte) (AccountCredits, error) {
	decoder := json.NewDecoder(bytes.NewReader(normalizeAdobeRefreshBody(body)))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return AccountCredits{}, errors.New("Adobe credits response returned invalid JSON")
	}
	totalInfo, ok := payload["total"].(map[string]any)
	if !ok {
		return AccountCredits{}, errors.New("Adobe credits response missing total")
	}
	quota, ok := totalInfo["quota"].(map[string]any)
	if !ok {
		return AccountCredits{}, errors.New("Adobe credits response missing total.quota")
	}
	total := adobeCreditNumber(quota["total"])
	used := adobeCreditNumber(quota["used"])
	available := adobeCreditNumber(quota["available"])
	if total == nil && used == nil && available == nil {
		return AccountCredits{}, errors.New("Adobe credits response contains no quota values")
	}
	availableUntil, _ := totalInfo["availableUntil"].(string)
	return AccountCredits{
		Total: total, Used: used, Available: available,
		AvailableUntil: strings.TrimSpace(availableUntil), UpdatedAt: time.Now().UTC(),
	}, nil
}

func adobeCreditNumber(value any) *float64 {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil
		}
		number = parsed
	case float64:
		number = typed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return nil
		}
		number = parsed
	default:
		return nil
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return nil
	}
	return &number
}

func (r *Runtime) RefreshAccountCredits(ctx context.Context, accountID string) (Account, error) {
	return r.refreshAccountCredits(ctx, accountID, true)
}

func (r *Runtime) refreshAccountCredits(ctx context.Context, accountID string, allowTokenRefresh bool) (Account, error) {
	session, err := r.repository.AccountSession(ctx, strings.TrimSpace(accountID))
	if err != nil {
		return Account{}, err
	}
	client, err := r.httpClientForRoute(ctx, session.Account.RouteAffinity, 25*time.Second)
	if err != nil {
		return Account{}, err
	}
	account, creditsErr := r.refreshAccountCreditsWithClient(ctx, client, session.Account, session.AccessToken)
	if creditsErr == nil || !isAdobeCreditsAuthError(creditsErr) {
		return account, creditsErr
	}
	if allowTokenRefresh && session.Account.Refreshable && cookieHeaderFromJar(session.CookieJar) != "" {
		if _, refreshErr := r.refreshAccountTokenOnly(ctx, session.Account.AccountID); refreshErr != nil {
			combinedErr := fmt.Errorf("Adobe access token is invalid and Cookie refresh failed: %w", refreshErr)
			updated, recordErr := r.repository.RecordAccountAuthFailure(ctx, session.Account.AccountID, combinedErr)
			if recordErr != nil {
				return Account{}, recordErr
			}
			return updated, combinedErr
		}
		return r.refreshAccountCredits(ctx, session.Account.AccountID, false)
	}
	updated, recordErr := r.repository.RecordAccountAuthFailure(ctx, session.Account.AccountID, creditsErr)
	if recordErr != nil {
		return Account{}, recordErr
	}
	return updated, creditsErr
}

func (r *Runtime) refreshAccountCreditsWithClient(ctx context.Context, client *http.Client, account Account, token string) (Account, error) {
	credits, err := fetchAdobeCredits(ctx, client, SessionSnapshot{
		AccountID: account.AccountID, AccessToken: token, UserAgent: account.ClientContext.UserAgent,
		AcceptLanguage: account.ClientContext.AcceptLanguage, Adobe2API: true,
	})
	if err != nil {
		updated, recordErr := r.repository.RecordAccountCreditsError(ctx, account.AccountID, err)
		if recordErr != nil {
			return Account{}, recordErr
		}
		return updated, err
	}
	return r.repository.UpdateAccountCredits(ctx, account.AccountID, credits)
}

func (r *Repository) UpdateAccountCredits(ctx context.Context, accountID string, credits AccountCredits) (Account, error) {
	var account Account
	err := scanAccount(r.db.QueryRow(ctx, `
UPDATE adobe_accounts SET credits_total=$2,credits_used=$3,credits_available=$4,credits_available_until=$5,
 credits_updated_at=$6,credits_error='',updated_at=NOW()
WHERE account_id=$1 RETURNING `+accountReturningSQL,
		strings.TrimSpace(accountID), credits.Total, credits.Used, credits.Available,
		strings.TrimSpace(credits.AvailableUntil), credits.UpdatedAt), &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return account, err
}

func (r *Repository) RecordAccountCreditsError(ctx context.Context, accountID string, creditsErr error) (Account, error) {
	message := truncateError(creditsErr)
	if len(message) > 300 {
		message = message[:300]
	}
	var account Account
	err := scanAccount(r.db.QueryRow(ctx, `
UPDATE adobe_accounts SET credits_updated_at=NOW(),credits_error=$2,updated_at=NOW()
WHERE account_id=$1 RETURNING `+accountReturningSQL, strings.TrimSpace(accountID), message), &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return account, err
}

func (r *Runtime) refreshCreditsAfterGeneration(accountID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = r.RefreshAccountCredits(ctx, accountID)
	}()
}
