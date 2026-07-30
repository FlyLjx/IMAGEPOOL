package adobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	proxyservice "imagepool/internal/proxy"
)

const (
	Adobe2APIRefreshProfileType = "adobe_refresh_profile"
	Adobe2APIRefreshURL         = "https://adobeid-na1.services.adobe.com/ims/check/v6/token?jslVersion=v2-v0.48.0-1-g1e322cb"
	Adobe2APIScope              = "AdobeID,firefly_api,openid"
	adobe2APIRegistrationPrefix = "adobe2api:"
)

type CompatibleImportItem struct {
	Name          string
	Email         string
	Token         string
	CookieHeader  string
	RefreshURL    string
	RouteAffinity string
	Disabled      bool
	ClientPolicy  ClientPolicy
}

type CompatibleImportFailure struct {
	Index int    `json:"index"`
	Name  string `json:"name,omitempty"`
	Error string `json:"error"`
}

type CompatibleImportResult struct {
	Status        string                    `json:"status"`
	Total         int                       `json:"total"`
	ImportedCount int                       `json:"imported_count"`
	FailedCount   int                       `json:"failed_count"`
	Items         []Account                 `json:"items"`
	Failures      []CompatibleImportFailure `json:"failures,omitempty"`
}

type compatibleAccountInput struct {
	CompatibleImportItem
	AccountID  string
	CookieJar  []Cookie
	Source     string
	CapturedAt time.Time
	ExpiresAt  *time.Time
	Policy     ClientPolicy
}

type adobeRefreshResult struct {
	AccessToken string
	ExpiresAt   *time.Time
	ExpiresIn   int64
}

type adobeRefreshPayload struct {
	AccessToken string
	ExpiresIn   int64
}

type adobeRefreshAuthError struct {
	message string
}

func (e *adobeRefreshAuthError) Error() string {
	return e.message
}

func newAdobeRefreshAuthError(message string) error {
	return &adobeRefreshAuthError{message: strings.TrimSpace(message)}
}

func isAdobeRefreshAuthError(err error) bool {
	var authErr *adobeRefreshAuthError
	return errors.As(err, &authErr)
}

func ParseAdobe2APIImport(data []byte) ([]CompatibleImportItem, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse Adobe import JSON: %w", err)
	}
	items, err := parseAdobe2APIValue(value, "")
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.New("no Adobe token or cookie profile found")
	}
	return items, nil
}

func parseAdobe2APIValue(value any, inheritedRoute string) ([]CompatibleImportItem, error) {
	switch typed := value.(type) {
	case string:
		token := cleanBearerToken(typed)
		if token == "" {
			return nil, nil
		}
		return []CompatibleImportItem{{Token: token, RouteAffinity: inheritedRoute}}, nil
	case []any:
		items := make([]CompatibleImportItem, 0, len(typed))
		for _, entry := range typed {
			parsed, err := parseAdobe2APIValue(entry, inheritedRoute)
			if err != nil {
				return nil, err
			}
			items = append(items, parsed...)
		}
		return items, nil
	case map[string]any:
		return parseAdobe2APIObject(typed, inheritedRoute)
	default:
		return nil, errors.New("Adobe import must be a JSON object or array")
	}
}

func parseAdobe2APIObject(object map[string]any, inheritedRoute string) ([]CompatibleImportItem, error) {
	routeID := firstString(object, "route_affinity", "route_id")
	if routeID == "" {
		routeID = inheritedRoute
	}
	if profiles, ok := object["profiles"].([]any); ok {
		return parseAdobe2APIValue(profiles, routeID)
	}
	if tokens, ok := object["tokens"].([]any); ok {
		return parseAdobe2APIValue(tokens, routeID)
	}
	if items, ok := object["items"].([]any); ok {
		return parseAdobe2APIValue(items, routeID)
	}

	name := firstString(object, "name", "display_name")
	email := firstString(object, "email")
	if account, ok := object["account"].(map[string]any); ok {
		if name == "" {
			name = firstString(account, "display_name", "name")
		}
		if email == "" {
			email = firstString(account, "email")
		}
	}
	status := strings.ToLower(firstString(object, "status"))
	token := cleanBearerToken(firstString(object, "value", "token", "access_token"))
	cookieHeader := cookieStringFromInput(object)
	refreshURL := Adobe2APIRefreshURL
	policy := ClientPolicy{}
	if headers, ok := object["headers"].(map[string]any); ok {
		policy = clientPolicyFromHeaders(policy, headers, false)
	}
	if headers, ok := object["firefly_headers"].(map[string]any); ok {
		policy = clientPolicyFromHeaders(policy, headers, false)
	}
	if endpoint, ok := object["endpoint"].(map[string]any); ok {
		if candidate := firstString(endpoint, "url"); candidate != "" {
			refreshURL = candidate
		}
		if headers, ok := endpoint["headers"].(map[string]any); ok {
			policy = clientPolicyFromHeaders(policy, headers, true)
			if cookieHeader == "" {
				cookieHeader = headerString(headers, "cookie")
			}
		}
	}
	if strings.EqualFold(firstString(object, "type"), Adobe2APIRefreshProfileType) && cookieHeader == "" {
		return nil, errors.New("adobe_refresh_profile endpoint.headers.Cookie is required")
	}
	if token == "" && cookieHeader == "" {
		return nil, nil
	}
	return []CompatibleImportItem{{
		Name: name, Email: email, Token: token, CookieHeader: cookieHeader,
		RefreshURL: refreshURL, RouteAffinity: routeID,
		Disabled:     status == "disabled" || status == "invalid" || status == "exhausted",
		ClientPolicy: policy,
	}}, nil
}

func clientPolicyFromHeaders(policy ClientPolicy, headers map[string]any, refresh bool) ClientPolicy {
	if refresh {
		if value := headerString(headers, "user-agent"); value != "" {
			policy.RefreshUserAgent = value
		}
		if value := headerString(headers, "accept-language"); value != "" {
			policy.RefreshAcceptLanguage = value
		}
		return policy
	}
	if value := headerString(headers, "user-agent"); value != "" {
		policy.UserAgent = value
	}
	if value := headerString(headers, "accept-language"); value != "" {
		policy.AcceptLanguage = value
	}
	if value := headerString(headers, "sec-ch-ua"); value != "" {
		policy.SecCHUA = value
	}
	return policy
}

func cookieStringFromInput(object map[string]any) string {
	for _, key := range []string{"cookie", "cookies"} {
		if raw, ok := object[key]; ok {
			return normalizeCookieInput(raw)
		}
	}
	return ""
}

func normalizeCookieInput(value any) string {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if strings.HasPrefix(strings.ToLower(text), "cookie:") {
			text = strings.TrimSpace(strings.SplitN(text, ":", 2)[1])
		}
		return text
	case []any:
		pairs := make([]string, 0, len(typed))
		for _, entry := range typed {
			switch item := entry.(type) {
			case string:
				if value := strings.TrimSpace(item); value != "" {
					pairs = append(pairs, value)
				}
			case map[string]any:
				if name := firstString(item, "name"); name != "" {
					pairs = append(pairs, name+"="+firstString(item, "value"))
				}
			}
		}
		return strings.Join(pairs, "; ")
	case map[string]any:
		if nested, ok := typed["cookie"]; ok {
			return normalizeCookieInput(nested)
		}
		if nested, ok := typed["cookies"]; ok {
			return normalizeCookieInput(nested)
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, key := range keys {
			pairs = append(pairs, key+"="+strings.TrimSpace(fmt.Sprint(typed[key])))
		}
		return strings.Join(pairs, "; ")
	default:
		return ""
	}
}

func headerString(headers map[string]any, target string) string {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), target) {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func cleanBearerToken(value string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "Bearer "))
}

func cookieJarFromHeader(header string) []Cookie {
	items := make([]Cookie, 0)
	seen := map[string]bool{}
	for _, part := range strings.Split(header, ";") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) != 2 {
			continue
		}
		name := strings.TrimSpace(pair[0])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		items = append(items, Cookie{Name: name, Value: strings.TrimSpace(pair[1]), Domain: ".adobe.com", Path: "/", Secure: true, SameSite: "Lax"})
	}
	return items
}

func cookieHeaderFromJar(cookies []Cookie) string {
	pairs := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if name := strings.TrimSpace(cookie.Name); name != "" {
			pairs = append(pairs, name+"="+cookie.Value)
		}
	}
	return strings.Join(pairs, "; ")
}

func validateAdobeRefreshURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = Adobe2APIRefreshURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "adobeid-na1.services.adobe.com") || !strings.HasPrefix(parsed.Path, "/ims/check/v6/token") {
		return "", errors.New("invalid Adobe refresh endpoint")
	}
	return parsed.String(), nil
}

func refreshAdobeAccessToken(ctx context.Context, client *http.Client, endpoint, cookieHeader string, policy ClientPolicy) (adobeRefreshResult, error) {
	endpoint, err := validateAdobeRefreshURL(endpoint)
	if err != nil {
		return adobeRefreshResult{}, err
	}
	if strings.TrimSpace(cookieHeader) == "" {
		return adobeRefreshResult{}, errors.New("cookie is required for token refresh")
	}
	form := url.Values{"client_id": {"projectx_webapp"}, "guest_allowed": {"true"}, "scope": {Adobe2APIScope}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return adobeRefreshResult{}, err
	}
	policy = normalizeClientPolicy(policy)
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Accept-Language", policy.RefreshAcceptLanguage)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	request.Header.Set("Cookie", cookieHeader)
	request.Header.Set("Origin", "https://new.express.adobe.com")
	request.Header.Set("Referer", "https://new.express.adobe.com/")
	request.Header.Set("User-Agent", policy.RefreshUserAgent)
	response, err := client.Do(request)
	if err != nil {
		return adobeRefreshResult{}, fmt.Errorf("Adobe token refresh request failed: %w", err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if readErr != nil {
		return adobeRefreshResult{}, readErr
	}
	if response.StatusCode != http.StatusOK {
		message := adobeRefreshErrorMessage(body)
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			if message == "" {
				message = "Adobe login cookie was rejected; import a fresh Cookie profile"
			}
			return adobeRefreshResult{}, newAdobeRefreshAuthError(fmt.Sprintf("Adobe token refresh returned HTTP %d: %s", response.StatusCode, message))
		}
		if message != "" {
			return adobeRefreshResult{}, fmt.Errorf("Adobe token refresh returned HTTP %d: %s", response.StatusCode, message)
		}
		return adobeRefreshResult{}, fmt.Errorf("Adobe token refresh returned HTTP %d", response.StatusCode)
	}
	payload, err := decodeAdobeRefreshPayload(body, response.Header.Get("Content-Type"))
	if err != nil {
		return adobeRefreshResult{}, err
	}
	payload.AccessToken = cleanBearerToken(payload.AccessToken)
	if payload.AccessToken == "" {
		return adobeRefreshResult{}, adobeMissingRefreshTokenError(body)
	}
	expiresAt := TokenExpiresAt(payload.AccessToken)
	if expiresAt == nil && payload.ExpiresIn > 0 {
		value := time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second)
		expiresAt = &value
	}
	return adobeRefreshResult{AccessToken: payload.AccessToken, ExpiresAt: expiresAt, ExpiresIn: payload.ExpiresIn}, nil
}

func adobeMissingRefreshTokenError(body []byte) error {
	if message := adobeRefreshErrorMessage(body); message != "" {
		return newAdobeRefreshAuthError("Adobe token refresh did not return access_token: " + message)
	}
	var payload map[string]any
	if json.Unmarshal(normalizeAdobeRefreshBody(body), &payload) == nil {
		if authenticated, ok := payload["authenticated"].(bool); ok && !authenticated {
			return newAdobeRefreshAuthError("Adobe login cookie is no longer authenticated; import a fresh Cookie profile")
		}
		status := strings.ToLower(firstString(payload, "status", "state"))
		if strings.Contains(status, "unauth") || strings.Contains(status, "invalid") || strings.Contains(status, "expired") {
			return newAdobeRefreshAuthError("Adobe login cookie is no longer authenticated; import a fresh Cookie profile")
		}
	}
	return newAdobeRefreshAuthError("Adobe token refresh did not return access_token; import a fresh Cookie profile")
}

func decodeAdobeRefreshPayload(body []byte, contentType string) (adobeRefreshPayload, error) {
	normalized := normalizeAdobeRefreshBody(body)
	metadata := fmt.Sprintf("content-type %q, %d bytes", sanitizeAdobeContentType(contentType), len(body))
	if len(normalized) == 0 {
		return adobeRefreshPayload{}, fmt.Errorf("Adobe token refresh returned an empty response (%s)", metadata)
	}
	if adobeResponseLooksHTML(normalized) {
		return adobeRefreshPayload{}, fmt.Errorf("Adobe token refresh returned an HTML login/challenge page (%s)", metadata)
	}

	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return adobeRefreshPayload{}, adobeInvalidJSONError(normalized, metadata)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return adobeRefreshPayload{}, adobeInvalidJSONError(normalized, metadata)
	}
	token, expiresIn := extractAdobeRefreshToken(value, 0)
	return adobeRefreshPayload{AccessToken: token, ExpiresIn: expiresIn}, nil
}

func adobeInvalidJSONError(body []byte, metadata string) error {
	category := "other"
	if len(body) > 0 && (body[0] == '{' || body[0] == '[') {
		category = "JSON-like"
	}
	return fmt.Errorf("Adobe token refresh returned invalid JSON (%s, body category %s)", metadata, category)
}

func normalizeAdobeRefreshBody(body []byte) []byte {
	body = bytes.TrimSpace(body)
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	body = bytes.TrimSpace(body)
	for _, prefix := range [][]byte{[]byte(")]}'" + ","), []byte(")]}'"), []byte("while(1);"), []byte("for(;;);")} {
		if bytes.HasPrefix(body, prefix) {
			return bytes.TrimSpace(body[len(prefix):])
		}
	}
	return body
}

func adobeResponseLooksHTML(body []byte) bool {
	prefix := body
	if len(prefix) > 256 {
		prefix = prefix[:256]
	}
	prefix = bytes.ToLower(bytes.TrimSpace(prefix))
	return bytes.HasPrefix(prefix, []byte("<!doctype html")) || bytes.HasPrefix(prefix, []byte("<html"))
}

func sanitizeAdobeContentType(value string) string {
	if mediaType, _, err := mime.ParseMediaType(value); err == nil && mediaType != "" {
		return strings.ToLower(mediaType)
	}
	value = strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	if value == "" {
		return "unknown"
	}
	if len(value) > 80 {
		value = value[:80]
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return "unknown"
		}
	}
	return strings.ToLower(value)
}

func extractAdobeRefreshToken(value any, depth int) (string, int64) {
	if depth > 4 {
		return "", 0
	}
	switch typed := value.(type) {
	case map[string]any:
		token := adobeRefreshTokenString(typed, "access_token", "accessToken")
		expiresIn := adobeExpiresIn(typed["expires_in"])
		if expiresIn == 0 {
			expiresIn = adobeExpiresIn(typed["expiresIn"])
		}
		if token != "" {
			return token, expiresIn
		}
		for _, key := range []string{"token", "data", "response", "result"} {
			if nested, ok := typed[key]; ok {
				if nestedToken, nestedExpiresIn := extractAdobeRefreshToken(nested, depth+1); nestedToken != "" {
					if nestedExpiresIn == 0 {
						nestedExpiresIn = expiresIn
					}
					return nestedToken, nestedExpiresIn
				}
			}
		}
	case []any:
		for _, item := range typed {
			if token, expiresIn := extractAdobeRefreshToken(item, depth+1); token != "" {
				return token, expiresIn
			}
		}
	}
	return "", 0
}

func adobeRefreshTokenString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok {
			if token := cleanBearerToken(value); token != "" {
				return token
			}
		}
	}
	return ""
}

func adobeExpiresIn(value any) int64 {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0
		}
		number = parsed
	case float64:
		number = typed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0
		}
		number = parsed
	default:
		return 0
	}
	if number <= 0 || number > float64(^uint64(0)>>1) {
		return 0
	}
	return int64(number)
}

func adobeRefreshErrorMessage(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(normalizeAdobeRefreshBody(body), &payload) != nil {
		return ""
	}
	message := firstString(payload, "error_description", "message", "error", "reason")
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}

func fetchAdobeAccountInfo(ctx context.Context, client *http.Client, token string) (displayName, email string) {
	for _, endpoint := range []string{"https://ims-na1.adobelogin.com/ims/profile/v1", "https://adobeid-na1.services.adobe.com/ims/profile/v1"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		request.Header.Set("Authorization", "Bearer "+cleanBearerToken(token))
		request.Header.Set("Accept", "application/json")
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil {
			continue
		}
		displayName = firstString(payload, "displayName", "name", "fullName")
		email = firstString(payload, "email")
		if displayName != "" || email != "" {
			return displayName, email
		}
	}
	return "", ""
}

func (r *Runtime) ImportAdobe2API(ctx context.Context, data []byte) (CompatibleImportResult, error) {
	items, err := ParseAdobe2APIImport(data)
	if err != nil {
		return CompatibleImportResult{}, err
	}
	result := CompatibleImportResult{Total: len(items), Items: make([]Account, 0, len(items))}
	for index, item := range items {
		account, importErr := r.importCompatibleItem(ctx, item)
		if importErr != nil {
			result.Failures = append(result.Failures, CompatibleImportFailure{Index: index, Name: item.Name, Error: importErr.Error()})
			continue
		}
		result.Items = append(result.Items, account)
	}
	result.ImportedCount = len(result.Items)
	result.FailedCount = len(result.Failures)
	result.Status = "ok"
	if result.FailedCount > 0 {
		result.Status = "partial"
	}
	if result.ImportedCount == 0 {
		result.Status = "failed"
		return result, errors.New("all Adobe account imports failed")
	}
	return result, nil
}

func (r *Runtime) importCompatibleItem(ctx context.Context, item CompatibleImportItem) (Account, error) {
	if r == nil || r.repository == nil {
		return Account{}, errors.New("Adobe runtime is not initialized")
	}
	policy := normalizeClientPolicy(item.ClientPolicy)
	routeID, err := r.repository.compatibleRoute(ctx, item.RouteAffinity)
	if err != nil {
		return Account{}, err
	}
	token := cleanBearerToken(item.Token)
	expiresAt := TokenExpiresAt(token)
	cookies := cookieJarFromHeader(item.CookieHeader)
	source := "token"
	if len(cookies) > 0 {
		source = "cookie"
		client, clientErr := r.httpClientForRoute(ctx, routeID, 35*time.Second)
		if clientErr != nil {
			return Account{}, clientErr
		}
		refreshed, refreshErr := refreshAdobeAccessToken(ctx, client, item.RefreshURL, item.CookieHeader, policy)
		if refreshErr != nil {
			return Account{}, refreshErr
		}
		token = refreshed.AccessToken
		expiresAt = refreshed.ExpiresAt
		displayName, email := fetchAdobeAccountInfo(ctx, client, token)
		if item.Name == "" {
			item.Name = displayName
		}
		if item.Email == "" {
			item.Email = email
		}
	}
	if token == "" {
		return Account{}, errors.New("Adobe access token is required")
	}
	accountID, err := AccountIDFromToken(token)
	if err != nil {
		return Account{}, fmt.Errorf("decode Adobe access token: %w", err)
	}
	input := compatibleAccountInput{
		CompatibleImportItem: item,
		AccountID:            accountID, CookieJar: cookies, Source: source, CapturedAt: time.Now().UTC(),
		ExpiresAt: expiresAt, Policy: policy,
	}
	input.Token = token
	input.RouteAffinity = routeID
	account, err := r.repository.upsertCompatibleAccount(ctx, input)
	if err != nil {
		return Account{}, err
	}
	creditsAccount, creditsErr := r.RefreshAccountCredits(ctx, account.AccountID)
	if creditsAccount.AccountID != "" {
		account = creditsAccount
	}
	// Credits are supplemental account metadata and do not invalidate a
	// successful token or cookie import.
	if creditsErr != nil {
		return account, nil
	}
	return creditsAccount, nil
}

func (r *Runtime) httpClientForRoute(ctx context.Context, routeID string, timeout time.Duration) (*http.Client, error) {
	if r.httpClientFactory != nil {
		return r.httpClientFactory(ctx, routeID, timeout)
	}
	routeURL, err := r.repository.RouteProxyURL(ctx, routeID)
	if err != nil {
		return nil, err
	}
	return proxyservice.NewHTTPClientForRuntime(adobeRouteProxyRuntime(routeURL), timeout)
}

func (r *Repository) compatibleRoute(ctx context.Context, preferred string) (string, error) {
	preferred = strings.TrimSpace(preferred)
	var routeID string
	query := `SELECT id FROM adobe_routes WHERE enabled=TRUE AND health_status IN ('unknown','healthy') AND (cooldown_until IS NULL OR cooldown_until<=NOW())`
	if preferred != "" {
		err := r.db.QueryRow(ctx, query+` AND id=$1`, preferred).Scan(&routeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("Adobe route %q is unavailable", preferred)
		}
		return routeID, err
	}
	err := r.db.QueryRow(ctx, query+` ORDER BY priority,(SELECT COUNT(*) FROM adobe_accounts WHERE route_id=adobe_routes.id),id LIMIT 1`).Scan(&routeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoAvailableRoute
	}
	return routeID, err
}

func (r *Repository) upsertCompatibleAccount(ctx context.Context, input compatibleAccountInput) (Account, error) {
	accountID := strings.TrimSpace(input.AccountID)
	if accountID == "" || cleanBearerToken(input.Token) == "" {
		return Account{}, errors.New("account id and access token are required")
	}
	cookiesJSON, err := json.Marshal(input.CookieJar)
	if err != nil {
		return Account{}, err
	}
	encryptedCookies, err := r.cipher.Encrypt(cookiesJSON, SecretAAD("adobe_accounts", accountID, "cookie_jar"))
	if err != nil {
		return Account{}, err
	}
	encryptedToken, err := r.cipher.Encrypt([]byte(cleanBearerToken(input.Token)), SecretAAD("adobe_accounts", accountID, "access_token"))
	if err != nil {
		return Account{}, err
	}
	policyJSON, err := json.Marshal(normalizeClientPolicy(input.Policy))
	if err != nil {
		return Account{}, err
	}
	profileID, err := randomID("adp_", 12)
	if err != nil {
		return Account{}, err
	}
	registrationID, err := randomID(adobe2APIRegistrationPrefix+input.Source+":", 10)
	if err != nil {
		return Account{}, err
	}
	profileRef := ""
	state := "ready"
	if input.Disabled {
		state = "disabled"
	}
	if input.CapturedAt.IsZero() {
		input.CapturedAt = time.Now().UTC()
	}
	_, err = r.db.Exec(ctx, `
INSERT INTO adobe_accounts(account_id,profile_id,state,registration_id,captured_at,email,display_name,cookie_jar_encrypted,access_token_encrypted,token_expires_at,client_context,route_id,browser_profile_ref,last_verified_at,disabled)
VALUES($1,$2,$3,$4,$5::timestamptz,$6,$7,$8,$9,$10,$11::jsonb,$12,$13,$5::timestamptz,$14)
ON CONFLICT(account_id) DO UPDATE SET
 state=EXCLUDED.state,registration_id=EXCLUDED.registration_id,captured_at=EXCLUDED.captured_at,email=CASE WHEN EXCLUDED.email='' THEN adobe_accounts.email ELSE EXCLUDED.email END,
 display_name=CASE WHEN EXCLUDED.display_name='' THEN adobe_accounts.display_name ELSE EXCLUDED.display_name END,
 cookie_jar_encrypted=CASE WHEN $15 THEN EXCLUDED.cookie_jar_encrypted ELSE adobe_accounts.cookie_jar_encrypted END,
 access_token_encrypted=EXCLUDED.access_token_encrypted,token_expires_at=EXCLUDED.token_expires_at,client_context=EXCLUDED.client_context,route_id=EXCLUDED.route_id,
 session_version=adobe_accounts.session_version+1,last_verified_at=EXCLUDED.last_verified_at,
 consecutive_failures=0,cooldown_until=NULL,last_error_code='',last_error='',disabled=EXCLUDED.disabled,updated_at=NOW()`,
		accountID, profileID, state, registrationID, input.CapturedAt, strings.TrimSpace(input.Email), strings.TrimSpace(input.Name), encryptedCookies, encryptedToken,
		input.ExpiresAt, string(policyJSON), input.RouteAffinity, profileRef, input.Disabled, len(input.CookieJar) > 0)
	if err != nil {
		return Account{}, err
	}
	return r.GetAccount(ctx, accountID)
}

func (r *Runtime) RefreshAccountToken(ctx context.Context, accountID string) (Account, error) {
	account, err := r.refreshAccountTokenOnly(ctx, accountID)
	if err != nil {
		if isAdobeRefreshAuthError(err) {
			if updated, recordErr := r.repository.RecordAccountAuthFailure(ctx, accountID, err); recordErr == nil {
				account = updated
			}
		}
		return account, err
	}
	creditsAccount, creditsErr := r.refreshAccountCredits(ctx, accountID, false)
	if creditsAccount.AccountID != "" {
		account = creditsAccount
	}
	if creditsErr != nil && isAdobeCreditsAuthError(creditsErr) {
		return account, creditsErr
	}
	return account, nil
}

func (r *Runtime) refreshAccountTokenOnly(ctx context.Context, accountID string) (Account, error) {
	var account Account
	err := r.repository.WithAccountLock(ctx, accountID, func(lockCtx context.Context) error {
		var refreshErr error
		account, refreshErr = r.refreshAccountTokenLocked(lockCtx, accountID)
		return refreshErr
	})
	return account, err
}

func (r *Runtime) refreshAccountTokenLocked(ctx context.Context, accountID string) (Account, error) {
	session, err := r.repository.AccountSession(ctx, accountID)
	if err != nil {
		return Account{}, err
	}
	cookieHeader := cookieHeaderFromJar(session.CookieJar)
	if cookieHeader == "" {
		return Account{}, newAdobeRefreshAuthError("this Adobe account has no refresh cookie profile")
	}
	client, err := r.httpClientForRoute(ctx, session.Account.RouteAffinity, 35*time.Second)
	if err != nil {
		return Account{}, err
	}
	refreshed, err := refreshAdobeAccessToken(ctx, client, Adobe2APIRefreshURL, cookieHeader, session.Account.ClientContext)
	if err != nil {
		return Account{}, err
	}
	refreshedAccountID, err := AccountIDFromToken(refreshed.AccessToken)
	if err != nil || refreshedAccountID != session.Account.AccountID {
		return Account{}, newAdobeRefreshAuthError("refreshed Adobe token belongs to a different account")
	}
	displayName, email := fetchAdobeAccountInfo(ctx, client, refreshed.AccessToken)
	account, err := r.repository.updateCompatibleToken(ctx, session.Account.AccountID, refreshed.AccessToken, refreshed.ExpiresAt, displayName, email)
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

func (r *Repository) updateCompatibleToken(ctx context.Context, accountID, token string, expiresAt *time.Time, displayName, email string) (Account, error) {
	encrypted, err := r.cipher.Encrypt([]byte(cleanBearerToken(token)), SecretAAD("adobe_accounts", strings.TrimSpace(accountID), "access_token"))
	if err != nil {
		return Account{}, err
	}
	var account Account
	err = scanAccount(r.db.QueryRow(ctx, `
UPDATE adobe_accounts SET access_token_encrypted=$2,token_expires_at=$3,captured_at=NOW(),last_verified_at=NOW(),session_version=session_version+1,
 email=CASE WHEN $4='' THEN email ELSE $4 END,display_name=CASE WHEN $5='' THEN display_name ELSE $5 END,
 state=CASE WHEN disabled THEN 'disabled' ELSE 'ready' END,consecutive_failures=0,cooldown_until=NULL,last_error_code='',last_error='',updated_at=NOW()
WHERE account_id=$1
RETURNING `+accountReturningSQL, strings.TrimSpace(accountID), encrypted, expiresAt, strings.TrimSpace(email), strings.TrimSpace(displayName)), &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return account, err
}

func (r *Repository) AccountsDueForCompatibleRefresh(ctx context.Context, interval time.Duration) ([]string, error) {
	if interval <= 0 {
		interval = 15 * time.Hour
	}
	rows, err := r.db.Query(ctx, `
SELECT account_id FROM adobe_accounts
WHERE registration_id LIKE 'adobe2api:cookie:%' AND disabled=FALSE
  AND (captured_at <= NOW()-make_interval(secs => $1) OR (token_expires_at IS NOT NULL AND token_expires_at <= NOW()+INTERVAL '1 hour'))
ORDER BY captured_at LIMIT 100`, int64(interval/time.Second))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func isAdobe2APIAccount(account Account) bool {
	return strings.HasPrefix(account.RegistrationID, adobe2APIRegistrationPrefix)
}
