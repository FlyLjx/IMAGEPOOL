package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	ErrDisabled       = errors.New("container updater is not configured")
	ErrInvalidVersion = errors.New("invalid release version")
	versionPattern    = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
)

type Status struct {
	Enabled       bool   `json:"enabled"`
	Updating      bool   `json:"updating"`
	TargetVersion string `json:"target_version,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

// Service triggers the internal Watchtower HTTP API. Watchtower performs the
// image pull and container replacement after this process has returned 202.
type Service struct {
	endpoint string
	token    string
	client   *http.Client
	delay    time.Duration

	mu            sync.RWMutex
	updating      bool
	targetVersion string
	lastError     string
}

func New(endpoint, token string) *Service {
	return &Service{
		endpoint: strings.TrimSpace(endpoint),
		token:    strings.TrimSpace(token),
		client:   &http.Client{Timeout: 15 * time.Second},
		delay:    time.Second,
	}
}

func NewFromEnvironment() *Service {
	return New(os.Getenv("IMAGE_POOL_UPDATE_URL"), os.Getenv("IMAGE_POOL_UPDATE_TOKEN"))
}

func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Status{
		Enabled:       s.endpoint != "",
		Updating:      s.updating,
		TargetVersion: s.targetVersion,
		LastError:     s.lastError,
	}
}

// Trigger schedules an update after the HTTP response can be delivered. The
// target image is selected by Watchtower from the Compose service definition.
func (s *Service) Trigger(version string) (Status, bool, error) {
	if s == nil || strings.TrimSpace(s.endpoint) == "" {
		return s.Status(), false, ErrDisabled
	}
	version = strings.TrimSpace(version)
	if !versionPattern.MatchString(version) {
		return s.Status(), false, ErrInvalidVersion
	}

	s.mu.Lock()
	if s.updating {
		status := s.statusLocked()
		s.mu.Unlock()
		return status, false, nil
	}
	s.updating = true
	s.targetVersion = strings.TrimPrefix(version, "v")
	s.lastError = ""
	status := s.statusLocked()
	s.mu.Unlock()

	go s.triggerWatchtower()
	return status, true, nil
}

func (s *Service) triggerWatchtower() {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	timeout := s.client.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, nil)
	if err == nil && s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	if err == nil {
		response, requestErr := s.client.Do(req)
		err = requestErr
		if response != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
			_ = response.Body.Close()
			if err == nil && (response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices) {
				err = fmt.Errorf("updater returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
			}
		}
	}

	s.mu.Lock()
	s.updating = false
	if err != nil {
		s.lastError = err.Error()
	}
	s.mu.Unlock()
}

func (s *Service) statusLocked() Status {
	return Status{
		Enabled:       s.endpoint != "",
		Updating:      s.updating,
		TargetVersion: s.targetVersion,
		LastError:     s.lastError,
	}
}
