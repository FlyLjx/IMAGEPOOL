package browsertransport

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"imagepool/internal/config"
)

func TestTLSClientRoundTripperUsesStandardHTTPContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "value" {
			t.Fatalf("header=%q", r.Header.Get("X-Test"))
		}
		w.Header().Set("X-Response", "ok")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewHTTPClient(config.ProxyRuntime{Enabled: true, EgressMode: "direct"}, 5*time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Test", "value")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.Header.Get("X-Response") != "ok" {
		t.Fatalf("response=%#v", response)
	}
}
