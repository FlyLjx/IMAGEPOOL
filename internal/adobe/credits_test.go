package adobe

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFetchAdobeCreditsMatchesAdobe2API(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != adobeCreditsURL {
			t.Fatalf("request=%s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("x-api-key") != adobeCreditsAPIKey || request.Header.Get("x-account-id") != "account-1" {
			t.Fatalf("headers=%v", request.Header)
		}
		body := `{"total":{"quota":{"total":250,"used":"12","available":238},"availableUntil":"2026-08-01T00:00:00Z"}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	credits, err := fetchAdobeCredits(context.Background(), client, SessionSnapshot{AccountID: "account-1", AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if credits.Total == nil || *credits.Total != 250 || credits.Used == nil || *credits.Used != 12 || credits.Available == nil || *credits.Available != 238 || credits.AvailableUntil == "" {
		t.Fatalf("credits=%#v", credits)
	}
}

func TestDecodeAdobeCreditsRejectsMissingQuota(t *testing.T) {
	_, err := decodeAdobeCredits([]byte(`{"total":{}}`))
	if err == nil || !strings.Contains(err.Error(), "total.quota") {
		t.Fatalf("error=%v", err)
	}
}

func TestFetchAdobeCreditsIncludesAdobeError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"error":"quota_error","error_description":"credits are unavailable"}`
		return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	_, err := fetchAdobeCredits(context.Background(), client, SessionSnapshot{AccountID: "account-1", AccessToken: "token"})
	if err == nil || !strings.Contains(err.Error(), "credits are unavailable") {
		t.Fatalf("error=%v", err)
	}
}

func TestFetchAdobeCreditsClassifiesAuthenticationFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"error_description":"Oauth token is not valid"}`
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	_, err := fetchAdobeCredits(context.Background(), client, SessionSnapshot{AccountID: "account-1", AccessToken: "revoked-token"})
	if !isAdobeCreditsAuthError(err) || !strings.Contains(err.Error(), "Oauth token is not valid") {
		t.Fatalf("error=%v", err)
	}
}
