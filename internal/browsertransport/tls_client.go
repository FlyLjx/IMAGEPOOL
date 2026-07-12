package browsertransport

import (
	"fmt"
	"net/http"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"

	"imagepool/internal/config"
	"imagepool/internal/proxy"
)

type roundTripper struct {
	client tlsclient.HttpClient
}

func NewHTTPClient(runtime config.ProxyRuntime, timeout time.Duration, resource bool) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profiles.Chrome_144),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithTimeoutMilliseconds(int(timeout.Milliseconds())),
	}
	if runtime.SkipSSLVerify {
		options = append(options, tlsclient.WithInsecureSkipVerify())
	}
	if proxyURL := proxy.EffectiveURL(runtime, resource); proxyURL != "" {
		if err := proxy.ValidateURL(proxyURL); err != nil {
			return nil, err
		}
		options = append(options, tlsclient.WithProxyUrl(proxyURL))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: timeout, Transport: &roundTripper{client: client}}, nil
}

func (t *roundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL == nil || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		return nil, fmt.Errorf("browser transport only supports HTTP(S) URLs")
	}
	upstreamRequest, err := fhttp.NewRequestWithContext(request.Context(), request.Method, request.URL.String(), request.Body)
	if err != nil {
		return nil, err
	}
	upstreamRequest.ContentLength = request.ContentLength
	upstreamRequest.Host = request.Host
	for key, values := range request.Header {
		for _, value := range values {
			upstreamRequest.Header.Add(key, value)
		}
	}
	upstreamResponse, err := t.client.Do(upstreamRequest)
	if err != nil {
		return nil, err
	}
	responseHeaders := make(http.Header, len(upstreamResponse.Header))
	for key, values := range upstreamResponse.Header {
		responseHeaders[key] = append([]string(nil), values...)
	}
	body := upstreamResponse.Body
	if body == nil {
		body = http.NoBody
	}
	return &http.Response{
		Status:        upstreamResponse.Status,
		StatusCode:    upstreamResponse.StatusCode,
		Proto:         upstreamResponse.Proto,
		ProtoMajor:    upstreamResponse.ProtoMajor,
		ProtoMinor:    upstreamResponse.ProtoMinor,
		Header:        responseHeaders,
		Body:          body,
		ContentLength: upstreamResponse.ContentLength,
		Request:       request,
	}, nil
}

var _ http.RoundTripper = (*roundTripper)(nil)
