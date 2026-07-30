package adobe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestParseAdobe2APITokensJSON(t *testing.T) {
	items, err := ParseAdobe2APIImport([]byte(`[
  {"id":"one","value":"token-one","status":"active"},
  {"id":"two","value":"Bearer token-two","status":"disabled"}
]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Token != "token-one" || items[0].Disabled || items[1].Token != "token-two" || !items[1].Disabled {
		t.Fatalf("items=%#v", items)
	}
}

func TestParseAdobe2APIMinimalCookieAndRefreshProfile(t *testing.T) {
	minimal, err := ParseAdobe2APIImport([]byte(`{"name":"account@example.com","cookie":"a=1; b=two=2","route_id":"route-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(minimal) != 1 || minimal[0].CookieHeader != "a=1; b=two=2" || minimal[0].RouteAffinity != "route-1" {
		t.Fatalf("minimal=%#v", minimal)
	}

	profile, err := ParseAdobe2APIImport([]byte(`{
  "type":"adobe_refresh_profile",
  "endpoint":{"url":"https://adobeid-na1.services.adobe.com/ims/check/v6/token?jslVersion=test","headers":{"Cookie":"session=secret","User-Agent":"profile-agent","Accept-Language":"en-US,en;q=0.9"}}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(profile) != 1 || profile[0].CookieHeader != "session=secret" || !strings.Contains(profile[0].RefreshURL, "jslVersion=test") || profile[0].ClientPolicy.RefreshUserAgent != "profile-agent" || profile[0].ClientPolicy.RefreshAcceptLanguage != "en-US,en;q=0.9" {
		t.Fatalf("profile=%#v", profile)
	}
}

func TestRefreshAdobeAccessTokenIncludesAdobeErrorDescription(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"error":"invalid_grant","error_description":"The supplied cookie is expired"}`
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	_, err := refreshAdobeAccessToken(context.Background(), client, Adobe2APIRefreshURL, "session=expired", ClientPolicy{})
	if err == nil || !strings.Contains(err.Error(), "The supplied cookie is expired") {
		t.Fatalf("error=%v", err)
	}
}

func TestParseAdobe2APIStoredRefreshProfiles(t *testing.T) {
	items, err := ParseAdobe2APIImport([]byte(`{
  "version":2,
  "route_affinity":"route-default",
  "profiles":[
    {"name":"first","endpoint":{"headers":{"Cookie":"a=1"}}},
    {"name":"second","endpoint":{"headers":{"Cookie":"b=2"}},"route_id":"route-2"}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].RouteAffinity != "route-default" || items[1].RouteAffinity != "route-2" {
		t.Fatalf("items=%#v", items)
	}
}

func TestRefreshAdobeAccessTokenMatchesOriginalRequest(t *testing.T) {
	token := jwtWithClaims(map[string]any{"user_id": "account-1", "exp": float64(time.Now().Add(time.Hour).Unix())})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "adobeid-na1.services.adobe.com" || request.Method != http.MethodPost {
			t.Fatalf("request=%s %s", request.Method, request.URL)
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), "client_id=projectx_webapp") || request.Header.Get("Cookie") != "session=secret" || request.Header.Get("Origin") != "https://new.express.adobe.com" || request.Header.Get("User-Agent") != "Mozilla/5.0" {
			t.Fatalf("headers=%v body=%s", request.Header, body)
		}
		payload, _ := json.Marshal(map[string]any{"access_token": token, "expires_in": 3600})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})}
	result, err := refreshAdobeAccessToken(context.Background(), client, Adobe2APIRefreshURL, "session=secret", ClientPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessToken != token || result.ExpiresAt == nil {
		t.Fatalf("result=%#v", result)
	}
}

func TestRefreshAdobeAccessTokenAcceptsNormalizedAndNestedJSON(t *testing.T) {
	token := jwtWithClaims(map[string]any{"user_id": "account-normalized", "exp": float64(time.Now().Add(time.Hour).Unix())})
	tests := []struct {
		name string
		body string
	}{
		{name: "utf8 bom", body: "\xef\xbb\xbf" + `{"access_token":"` + token + `","expires_in":3600}`},
		{name: "xssi prefix", body: ")]}'\n" + `{"access_token":"` + token + `","expires_in":3600}`},
		{name: "nested token", body: `{"data":{"token":{"accessToken":"` + token + `","expiresIn":"3600"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := refreshResponseClient(http.StatusOK, "application/json; charset=utf-8", test.body)
			result, err := refreshAdobeAccessToken(context.Background(), client, Adobe2APIRefreshURL, "session=secret", ClientPolicy{})
			if err != nil {
				t.Fatal(err)
			}
			if result.AccessToken != token || result.ExpiresIn != 3600 {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}

func TestRefreshAdobeAccessTokenClassifiesNonJSONResponses(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{name: "empty", contentType: "application/json", body: " \r\n", want: "empty response"},
		{name: "html", contentType: "text/html; charset=utf-8", body: "<!doctype html><title>Sign in</title>", want: "HTML login/challenge page"},
		{name: "malformed json", contentType: "application/json", body: `{"access_token":`, want: "body category JSON-like"},
		{name: "trailing content", contentType: "application/json", body: `{"access_token":"value"} trailing`, want: "body category JSON-like"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := refreshResponseClient(http.StatusOK, test.contentType, test.body)
			_, err := refreshAdobeAccessToken(context.Background(), client, Adobe2APIRefreshURL, "session=secret", ClientPolicy{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), test.body) && strings.TrimSpace(test.body) != "" {
				t.Fatalf("response body leaked in error: %v", err)
			}
		})
	}
}

func TestRefreshAdobeAccessTokenClassifiesUnauthenticatedJSON(t *testing.T) {
	client := refreshResponseClient(http.StatusOK, "application/json", `{"authenticated":false}`)
	_, err := refreshAdobeAccessToken(context.Background(), client, Adobe2APIRefreshURL, "session=expired", ClientPolicy{})
	if err == nil || !isAdobeRefreshAuthError(err) || !strings.Contains(err.Error(), "fresh Cookie profile") {
		t.Fatalf("error=%v", err)
	}
}

func refreshResponseClient(status int, contentType, body string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", contentType)
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func TestTokenExpiresAtSupportsAdobeCreatedAtClaims(t *testing.T) {
	token := jwtWithClaims(map[string]any{"user_id": "account-1", "created_at": 1000.0, "expires_in": 3600.0})
	expiresAt := TokenExpiresAt(token)
	if expiresAt == nil || expiresAt.Unix() != 4600 {
		t.Fatalf("expires_at=%v", expiresAt)
	}
}

func TestAdobe2APIHeadersMatchOriginalClient(t *testing.T) {
	request, _ := http.NewRequest(http.MethodPost, imageSubmitURL, nil)
	applyAdobeHeaders(request, SessionSnapshot{AccessToken: "token", UserAgent: "ua", AcceptLanguage: "en", Adobe2API: true})
	if request.Header.Get("x-api-key") != adobe2APIKey || request.Header.Get("Origin") != "https://new.express.adobe.com" || request.Header.Get("x-arp-session-id") != "" {
		t.Fatalf("headers=%v", request.Header)
	}
}

func jwtWithClaims(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
