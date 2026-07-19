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
	ErrPinnedImageTag = errors.New("当前部署固定了 IMAGE_POOL_IMAGE_TAG，在线更新需要 IMAGE_POOL_IMAGE_TAG=latest")
	versionPattern    = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
)

type Status struct {
	Enabled       bool   `json:"enabled"`
	Updating      bool   `json:"updating"`
	TargetVersion string `json:"target_version,omitempty"`
	Image         string `json:"image,omitempty"`
	ImageTag      string `json:"image_tag,omitempty"`
	UpdateMode    string `json:"update_mode,omitempty"`
	Warning       string `json:"warning,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

// Service triggers the internal Watchtower HTTP API. Watchtower performs the
// image pull and container replacement after this process has returned 202.
type Service struct {
	endpoint string
	token    string
	client   *http.Client
	delay    time.Duration
	image    string
	imageTag string

	mu            sync.RWMutex
	updating      bool
	targetVersion string
	lastError     string
}

func New(endpoint, token string) *Service {
	return NewWithImage(endpoint, token, "", "")
}

func NewWithImage(endpoint, token, image, imageTag string) *Service {
	return &Service{
		endpoint: strings.TrimSpace(endpoint),
		token:    strings.TrimSpace(token),
		client:   &http.Client{Timeout: 15 * time.Second},
		delay:    time.Second,
		image:    strings.TrimSpace(image),
		imageTag: strings.TrimSpace(imageTag),
	}
}

func NewFromEnvironment() *Service {
	return NewWithImage(
		os.Getenv("IMAGE_POOL_UPDATE_URL"),
		os.Getenv("IMAGE_POOL_UPDATE_TOKEN"),
		os.Getenv("IMAGE_POOL_IMAGE"),
		os.Getenv("IMAGE_POOL_IMAGE_TAG"),
	)
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
		Image:         s.image,
		ImageTag:      s.imageTag,
		UpdateMode:    s.updateMode(),
		Warning:       s.warning(),
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
	if s.pinnedImageTag() {
		status := s.Status()
		status.LastError = ErrPinnedImageTag.Error()
		return status, false, ErrPinnedImageTag
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
		Image:         s.image,
		ImageTag:      s.imageTag,
		UpdateMode:    s.updateMode(),
		Warning:       s.warning(),
		LastError:     s.lastError,
	}
}

func (s *Service) pinnedImageTag() bool {
	if s == nil {
		return false
	}
	tag := strings.ToLower(strings.TrimSpace(s.imageTag))
	return tag != "" && tag != "latest"
}

func (s *Service) updateMode() string {
	if s == nil || strings.TrimSpace(s.endpoint) == "" {
		return "disabled"
	}
	tag := strings.ToLower(strings.TrimSpace(s.imageTag))
	if tag == "" {
		return "watchtower"
	}
	if tag == "latest" {
		return "watchtower_latest"
	}
	return "pinned"
}

func (s *Service) warning() string {
	if s.pinnedImageTag() {
		return ErrPinnedImageTag.Error()
	}
	return ""
}
