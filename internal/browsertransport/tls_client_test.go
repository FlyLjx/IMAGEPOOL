package browsertransport

import (
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestStreamingClientSharesCookieJarWithoutTotalTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	runtime := config.ProxyRuntime{Enabled: true, EgressMode: "direct"}
	normal, err := NewHTTPClient(runtime, 5*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := NewStreamingHTTPClient(runtime, false, CookieJarForHTTPClient(normal))
	if err != nil {
		t.Fatal(err)
	}
	if stream.Timeout != 0 {
		t.Fatalf("stream timeout=%s", stream.Timeout)
	}
	if got := tlsClientRequestTimeout(t, stream); got != 0 {
		t.Fatalf("TLS stream deadline=%s, want disabled", got)
	}
	if CookieJarForHTTPClient(stream) != CookieJarForHTTPClient(normal) {
		t.Fatal("stream client did not retain the browser cookie jar")
	}
	response, err := stream.Get(server.URL)
	if err != nil {
		t.Fatalf("stream request inherited normal timeout: %v", err)
	}
	response.Body.Close()
}

func tlsClientRequestTimeout(t *testing.T, client *http.Client) time.Duration {
	t.Helper()
	transport, ok := client.Transport.(*roundTripper)
	if !ok {
		t.Fatalf("transport=%T", client.Transport)
	}
	value := reflect.ValueOf(transport.client)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		t.Fatalf("TLS client=%T", transport.client)
	}
	value = value.Elem().FieldByName("Client").FieldByName("Timeout")
	if !value.IsValid() || value.Kind() != reflect.Int64 {
		t.Fatalf("TLS client timeout field unavailable: %s", value.Kind())
	}
	return time.Duration(value.Int())
}
