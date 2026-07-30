package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"imagepool/internal/openaiweb"
)

const (
	maxConcurrentReferenceDownloads = 4
	maxReferenceDownloadTimeout     = 30 * time.Second
	referenceDownloadDialTimeout    = 10 * time.Second
	referenceResponseHeaderTimeout  = 15 * time.Second
)

// referenceImageHTTPClient is shared by all edit requests. Its transport keeps
// idle connections for common image hosts while context deadlines still bound
// each individual edit request.
var referenceImageHTTPClient = newReferenceImageHTTPClient()
var publicReferenceImageHTTPClient = newPublicReferenceImageHTTPClient()

func newReferenceImageHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   referenceDownloadDialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = referenceDownloadDialTimeout
	transport.ResponseHeaderTimeout = referenceResponseHeaderTimeout
	transport.MaxIdleConns = 200
	transport.MaxIdleConnsPerHost = 64
	transport.IdleConnTimeout = 90 * time.Second

	return &http.Client{
		Transport: transport,
		Timeout:   maxReferenceDownloadTimeout,
	}
}

func newPublicReferenceImageHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = publicReferenceDialContext
	transport.TLSHandshakeTimeout = referenceDownloadDialTimeout
	transport.ResponseHeaderTimeout = referenceResponseHeaderTimeout
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 16
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{
		Transport: transport,
		Timeout:   maxReferenceDownloadTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many image redirects")
			}
			return validatePublicReferenceURL(req.URL)
		},
	}
}

func publicReferenceDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("image host %q did not resolve", host)
	}
	for _, address := range addresses {
		if !isPublicReferenceIP(address.IP) {
			return nil, errors.New("image URL resolves to a private or local address")
		}
	}
	dialer := &net.Dialer{Timeout: referenceDownloadDialTimeout, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
}

func validatePublicReferenceSource(source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return errors.New("empty image source")
	}
	if strings.HasPrefix(strings.ToLower(source), "data:") || (!strings.Contains(source, "://") && !strings.HasPrefix(source, "//")) {
		return nil
	}
	parsed, err := url.Parse(source)
	if err != nil {
		return errors.New("invalid image URL")
	}
	return validatePublicReferenceURL(parsed)
}

func validatePublicReferenceURL(parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("image URL must be an HTTP or HTTPS URL without credentials")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("image URL cannot target localhost")
	}
	if ip := net.ParseIP(host); ip != nil && !isPublicReferenceIP(ip) {
		return errors.New("image URL cannot target a private or local address")
	}
	return nil
}

func isPublicReferenceIP(ip net.IP) bool {
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func (s *Server) referenceDownloadTimeout() time.Duration {
	timeout := time.Duration(s.currentConfig().RequestTimeoutSecs * float64(time.Second))
	if timeout <= 0 || timeout > maxReferenceDownloadTimeout {
		return maxReferenceDownloadTimeout
	}
	return timeout
}

func (s *Server) imageInputsFromSources(ctx context.Context, sources []string) ([]openaiweb.ImageInput, error) {
	return s.imageInputsFromSourcesWithClient(ctx, sources, referenceImageHTTPClient, false)
}

func (s *Server) publicImageInputsFromSources(ctx context.Context, sources []string) ([]openaiweb.ImageInput, error) {
	return s.imageInputsFromSourcesWithClient(ctx, sources, publicReferenceImageHTTPClient, true)
}

func (s *Server) imageInputsFromSourcesWithClient(ctx context.Context, sources []string, client *http.Client, validatePublic bool) ([]openaiweb.ImageInput, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	if validatePublic {
		for _, source := range sources {
			if err := validatePublicReferenceSource(source); err != nil {
				return nil, err
			}
		}
	}

	// One deadline covers the complete reference set, rather than granting every
	// source a fresh timeout. A failed or canceled source stops its siblings.
	ctx, cancel := context.WithTimeout(ctx, s.referenceDownloadTimeout())
	defer cancel()

	results := make([]openaiweb.ImageInput, len(sources))
	workers := min(maxConcurrentReferenceDownloads, len(sources))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error

	setErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		errMu.Unlock()
	}

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					image, err := openaiweb.ImageInputFromSource(ctx, client, sources[index])
					if err != nil {
						setErr(err)
						return
					}
					results[index] = image
				}
			}
		}()
	}

	for index := range sources {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			errMu.Lock()
			err := firstErr
			errMu.Unlock()
			if err != nil {
				return nil, err
			}
			return nil, ctx.Err()
		case jobs <- index:
		}
	}
	close(jobs)
	wg.Wait()

	errMu.Lock()
	err := firstErr
	errMu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
