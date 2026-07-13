package httpapi

import (
	"context"
	"net"
	"net/http"
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

func (s *Server) referenceDownloadTimeout() time.Duration {
	timeout := time.Duration(s.currentConfig().RequestTimeoutSecs * float64(time.Second))
	if timeout <= 0 || timeout > maxReferenceDownloadTimeout {
		return maxReferenceDownloadTimeout
	}
	return timeout
}

func (s *Server) imageInputsFromSources(ctx context.Context, sources []string) ([]openaiweb.ImageInput, error) {
	if len(sources) == 0 {
		return nil, nil
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
					image, err := openaiweb.ImageInputFromSource(ctx, referenceImageHTTPClient, sources[index])
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
