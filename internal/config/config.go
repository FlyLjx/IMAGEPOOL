package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	AppName                             string        `json:"app_name"`
	ListenAddr                          string        `json:"listen_addr"`
	Timezone                            string        `json:"timezone"`
	StorageBackend                      string        `json:"storage_backend"`
	DatabaseURL                         string        `json:"database_url"`
	APIKeys                             []string      `json:"api_keys"`
	AuthKeyFile                         string        `json:"auth_key_file"`
	AccountFile                         string        `json:"account_file"`
	CallLogFile                         string        `json:"call_log_file"`
	ImageTagsFile                       string        `json:"image_tags_file"`
	RegisterFile                        string        `json:"register_file"`
	WebDistDir                          string        `json:"web_dist_dir"`
	ImageOutputDir                      string        `json:"image_output_dir"`
	ChatGPTBaseURL                      string        `json:"chatgpt_base_url"`
	UpstreamTransport                   string        `json:"upstream_transport"`
	ImageWebModelSlug                   string        `json:"image_web_model_slug"`
	ImagePollTimeoutSecs                float64       `json:"image_poll_timeout_secs"`
	ImagePollIntervalSecs               float64       `json:"image_poll_interval_secs"`
	ImagePollInitialWaitSecs            float64       `json:"image_poll_initial_wait_secs"`
	ImageSettleSecs                     float64       `json:"image_settle_secs"`
	ImageAccountPrecheckIntervalMinutes int           `json:"image_account_precheck_interval_minutes"`
	ImageAccountPrecheckConcurrency     int           `json:"image_account_precheck_concurrency"`
	ImageAccountPrecheckTimeoutSecs     float64       `json:"image_account_precheck_timeout_secs"`
	ImageCheckBeforeHitEnabled          bool          `json:"image_check_before_hit_enabled"`
	ImageSettleEnabled                  bool          `json:"image_settle_enabled"`
	MaxImageAttempts                    int           `json:"max_image_attempts"`
	RequestTimeoutSecs                  float64       `json:"request_timeout_secs"`
	SearchModel                         string        `json:"search_model"`
	SearchTimeoutSecs                   float64       `json:"search_timeout_secs"`
	SearchPollIntervalSecs              float64       `json:"search_poll_interval_secs"`
	RefreshAccountConcurrency           int           `json:"refresh_account_concurrency"`
	Proxy                               string        `json:"proxy"`
	ProxyRuntime                        ProxyRuntime  `json:"proxy_runtime"`
	Notifications                       Notifications `json:"notifications"`
	Models                              []string      `json:"models"`
	sourcePath                          string
}

type Notifications struct {
	Bark BarkNotification `json:"bark"`
}

type BarkNotification struct {
	Enabled                  bool   `json:"enabled"`
	ServerURL                string `json:"server_url"`
	DeviceKey                string `json:"device_key"`
	TitlePrefix              string `json:"title_prefix"`
	Group                    string `json:"group"`
	Level                    string `json:"level"`
	TimeoutSecs              int    `json:"timeout_secs"`
	MinIntervalSeconds       int    `json:"min_interval_seconds"`
	NotifyFailedCalls        bool   `json:"notify_failed_calls"`
	NotifyRegister           bool   `json:"notify_register"`
	NotifyRegisterErrorsOnly bool   `json:"notify_register_errors_only"`
	NotifyAutoRefill         bool   `json:"notify_auto_refill"`
}

type ProxyRuntime struct {
	Enabled                 bool             `json:"enabled"`
	EgressMode              string           `json:"egress_mode"`
	ProxyURL                string           `json:"proxy_url"`
	ResourceProxyURL        string           `json:"resource_proxy_url"`
	SkipSSLVerify           bool             `json:"skip_ssl_verify"`
	ResetSessionStatusCodes []int            `json:"reset_session_status_codes"`
	Clearance               ClearanceRuntime `json:"clearance"`
}

type ClearanceRuntime struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	CFCookies       string `json:"cf_cookies"`
	CFClearance     string `json:"cf_clearance"`
	UserAgent       string `json:"user_agent"`
	Browser         string `json:"browser"`
	FlareSolverrURL string `json:"flaresolverr_url"`
	TimeoutSec      int    `json:"timeout_sec"`
	RefreshInterval int    `json:"refresh_interval"`
	WarmUpOnStart   bool   `json:"warm_up_on_start"`
}

func Default() Config {
	return Config{
		AppName:                             "IMAGE POOL",
		ListenAddr:                          ":8080",
		Timezone:                            "Asia/Shanghai",
		StorageBackend:                      "postgres",
		DatabaseURL:                         "postgresql://imagepool:imagepool@postgres:5432/imagepool?sslmode=disable",
		APIKeys:                             []string{"dev-key"},
		AuthKeyFile:                         "data/auth_keys.json",
		AccountFile:                         "data/accounts.json",
		CallLogFile:                         "data/calls.json",
		ImageTagsFile:                       "data/image_tags.json",
		RegisterFile:                        "data/register.json",
		WebDistDir:                          "web_dist",
		ImageOutputDir:                      "data/images",
		ChatGPTBaseURL:                      "https://chatgpt.com",
		UpstreamTransport:                   "standard",
		ImageWebModelSlug:                   "gpt-5-5",
		ImagePollTimeoutSecs:                90,
		ImagePollIntervalSecs:               3,
		ImagePollInitialWaitSecs:            0,
		ImageSettleSecs:                     2,
		ImageAccountPrecheckIntervalMinutes: 10,
		ImageAccountPrecheckConcurrency:     6,
		ImageAccountPrecheckTimeoutSecs:     75,
		ImageCheckBeforeHitEnabled:          true,
		ImageSettleEnabled:                  true,
		MaxImageAttempts:                    3,
		RequestTimeoutSecs:                  120,
		SearchModel:                         "gpt-5-5",
		SearchTimeoutSecs:                   300,
		SearchPollIntervalSecs:              3,
		RefreshAccountConcurrency:           8,
		Notifications:                       Notifications{Bark: BarkNotification{ServerURL: "https://api.day.app", TitlePrefix: "IMAGE POOL", Group: "image-pool", Level: "active", TimeoutSecs: 10, MinIntervalSeconds: 60, NotifyFailedCalls: true, NotifyRegister: true, NotifyAutoRefill: true}},
		ProxyRuntime:                        ProxyRuntime{Enabled: true, EgressMode: "direct", ResetSessionStatusCodes: []int{403}, Clearance: ClearanceRuntime{Enabled: false, Mode: "none", Browser: "chrome", TimeoutSec: 60, RefreshInterval: 3600}},
		Models:                              []string{"gpt-image-2", "codex-gpt-image-2", "plus-codex-gpt-image-2", "team-codex-gpt-image-2", "pro-codex-gpt-image-2"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if strings.TrimSpace(path) == "" {
		return cfg.Normalize(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	base := filepath.Dir(path)
	if !filepath.IsAbs(cfg.AccountFile) {
		cfg.AccountFile = filepath.Clean(filepath.Join(base, cfg.AccountFile))
	}
	if !filepath.IsAbs(cfg.AuthKeyFile) {
		cfg.AuthKeyFile = filepath.Clean(filepath.Join(base, cfg.AuthKeyFile))
	}
	if !filepath.IsAbs(cfg.CallLogFile) {
		cfg.CallLogFile = filepath.Clean(filepath.Join(base, cfg.CallLogFile))
	}
	if !filepath.IsAbs(cfg.ImageTagsFile) {
		cfg.ImageTagsFile = filepath.Clean(filepath.Join(base, cfg.ImageTagsFile))
	}
	if !filepath.IsAbs(cfg.RegisterFile) {
		cfg.RegisterFile = filepath.Clean(filepath.Join(base, cfg.RegisterFile))
	}
	if !filepath.IsAbs(cfg.ImageOutputDir) {
		cfg.ImageOutputDir = filepath.Clean(filepath.Join(base, cfg.ImageOutputDir))
	}
	if !filepath.IsAbs(cfg.WebDistDir) {
		cfg.WebDistDir = filepath.Clean(filepath.Join(base, cfg.WebDistDir))
	}
	cfg = cfg.Normalize()
	cfg.sourcePath = filepath.Clean(path)
	return cfg, nil
}

func LoadIfExists(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Default().Normalize(), nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			cfg := Default().Normalize()
			cfg.sourcePath = filepath.Clean(path)
			return cfg, nil
		}
		return Config{}, err
	}
	return Load(path)
}

func (c Config) SourcePath() string { return c.sourcePath }

func (c Config) Save() error {
	path := strings.TrimSpace(c.sourcePath)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c.Normalize(), "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c Config) Normalize() Config {
	d := Default()
	if strings.TrimSpace(c.AppName) == "" {
		c.AppName = d.AppName
	}
	if strings.TrimSpace(c.ListenAddr) == "" {
		c.ListenAddr = d.ListenAddr
	}
	c.Timezone = strings.TrimSpace(c.Timezone)
	if c.Timezone == "" {
		c.Timezone = d.Timezone
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		c.Timezone = d.Timezone
	}
	c.StorageBackend = strings.ToLower(strings.TrimSpace(c.StorageBackend))
	if c.StorageBackend == "" {
		c.StorageBackend = d.StorageBackend
	}
	if c.StorageBackend != "postgres" && c.StorageBackend != "postgresql" && c.StorageBackend != "json" {
		c.StorageBackend = d.StorageBackend
	}
	c.DatabaseURL = strings.TrimSpace(c.DatabaseURL)
	if value := strings.TrimSpace(os.Getenv("DATABASE_URL")); value != "" {
		c.DatabaseURL = value
	}
	if c.StorageBackend == "postgres" || c.StorageBackend == "postgresql" {
		if c.DatabaseURL == "" {
			c.DatabaseURL = d.DatabaseURL
		}
	}
	c.UpstreamTransport = strings.ToLower(strings.TrimSpace(c.UpstreamTransport))
	if value := strings.TrimSpace(os.Getenv("IMAGE_POOL_UPSTREAM_TRANSPORT")); value != "" {
		c.UpstreamTransport = strings.ToLower(value)
	}
	if c.UpstreamTransport != "tls_client" {
		c.UpstreamTransport = d.UpstreamTransport
	}
	if strings.TrimSpace(c.AccountFile) == "" {
		c.AccountFile = d.AccountFile
	}
	if strings.TrimSpace(c.AuthKeyFile) == "" {
		c.AuthKeyFile = d.AuthKeyFile
	}
	if strings.TrimSpace(c.CallLogFile) == "" {
		c.CallLogFile = d.CallLogFile
	}
	if strings.TrimSpace(c.ImageTagsFile) == "" {
		c.ImageTagsFile = d.ImageTagsFile
	}
	if strings.TrimSpace(c.RegisterFile) == "" {
		c.RegisterFile = d.RegisterFile
	}
	if strings.TrimSpace(c.WebDistDir) == "" {
		c.WebDistDir = d.WebDistDir
	}
	if strings.TrimSpace(c.ImageOutputDir) == "" {
		c.ImageOutputDir = d.ImageOutputDir
	}
	if strings.TrimSpace(c.ChatGPTBaseURL) == "" {
		c.ChatGPTBaseURL = d.ChatGPTBaseURL
	}
	c.ChatGPTBaseURL = strings.TrimRight(strings.TrimSpace(c.ChatGPTBaseURL), "/")
	if strings.TrimSpace(c.ImageWebModelSlug) == "" {
		c.ImageWebModelSlug = d.ImageWebModelSlug
	}
	if c.ImagePollTimeoutSecs <= 0 {
		c.ImagePollTimeoutSecs = d.ImagePollTimeoutSecs
	}
	if c.ImagePollIntervalSecs <= 0 {
		c.ImagePollIntervalSecs = d.ImagePollIntervalSecs
	}
	if c.ImagePollInitialWaitSecs < 0 {
		c.ImagePollInitialWaitSecs = 0
	}
	if c.ImageSettleSecs < 0 {
		c.ImageSettleSecs = 0
	}
	if c.ImageAccountPrecheckIntervalMinutes <= 0 {
		c.ImageAccountPrecheckIntervalMinutes = d.ImageAccountPrecheckIntervalMinutes
	}
	if c.ImageAccountPrecheckConcurrency <= 0 {
		c.ImageAccountPrecheckConcurrency = d.ImageAccountPrecheckConcurrency
	}
	if c.ImageAccountPrecheckConcurrency > 30 {
		c.ImageAccountPrecheckConcurrency = 30
	}
	if c.ImageAccountPrecheckTimeoutSecs <= 0 {
		c.ImageAccountPrecheckTimeoutSecs = d.ImageAccountPrecheckTimeoutSecs
	}
	if c.ImageAccountPrecheckTimeoutSecs > 180 {
		c.ImageAccountPrecheckTimeoutSecs = 180
	}
	if c.MaxImageAttempts <= 0 {
		c.MaxImageAttempts = d.MaxImageAttempts
	}
	if c.RequestTimeoutSecs <= 0 {
		c.RequestTimeoutSecs = d.RequestTimeoutSecs
	}
	if strings.TrimSpace(c.SearchModel) == "" {
		c.SearchModel = d.SearchModel
	}
	if c.SearchTimeoutSecs <= 0 {
		c.SearchTimeoutSecs = d.SearchTimeoutSecs
	}
	if c.SearchPollIntervalSecs <= 0 {
		c.SearchPollIntervalSecs = d.SearchPollIntervalSecs
	}
	if c.RefreshAccountConcurrency <= 0 {
		c.RefreshAccountConcurrency = d.RefreshAccountConcurrency
	}
	c.Proxy = strings.TrimSpace(c.Proxy)
	if proxyRuntimeEmpty(c.ProxyRuntime) {
		c.ProxyRuntime = d.ProxyRuntime
	}
	c.ProxyRuntime = normalizeProxyRuntime(c.ProxyRuntime, c.Proxy)
	c.Notifications = normalizeNotifications(c.Notifications)
	if len(c.Models) == 0 {
		c.Models = append([]string(nil), d.Models...)
	}
	keys := make([]string, 0, len(c.APIKeys))
	seen := map[string]bool{}
	for _, k := range c.APIKeys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		keys = append([]string(nil), d.APIKeys...)
	}
	c.APIKeys = keys
	return c
}

func normalizeNotifications(value Notifications) Notifications {
	defaults := Default().Notifications.Bark
	bark := value.Bark
	bark.ServerURL = strings.TrimRight(strings.TrimSpace(bark.ServerURL), "/")
	if bark.ServerURL == "" {
		bark.ServerURL = defaults.ServerURL
	}
	bark.DeviceKey = strings.TrimSpace(bark.DeviceKey)
	bark.TitlePrefix = strings.TrimSpace(bark.TitlePrefix)
	if bark.TitlePrefix == "" {
		bark.TitlePrefix = defaults.TitlePrefix
	}
	bark.Group = strings.TrimSpace(bark.Group)
	if bark.Group == "" {
		bark.Group = defaults.Group
	}
	bark.Level = strings.TrimSpace(bark.Level)
	switch bark.Level {
	case "active", "timeSensitive", "passive", "critical":
	default:
		bark.Level = defaults.Level
	}
	if bark.TimeoutSecs <= 0 {
		bark.TimeoutSecs = defaults.TimeoutSecs
	}
	if bark.TimeoutSecs > 60 {
		bark.TimeoutSecs = 60
	}
	if bark.MinIntervalSeconds < 0 {
		bark.MinIntervalSeconds = defaults.MinIntervalSeconds
	}
	if bark.MinIntervalSeconds > 3600 {
		bark.MinIntervalSeconds = 3600
	}
	return Notifications{Bark: bark}
}

func proxyRuntimeEmpty(value ProxyRuntime) bool {
	return !value.Enabled && value.EgressMode == "" && value.ProxyURL == "" && value.ResourceProxyURL == "" && !value.SkipSSLVerify && len(value.ResetSessionStatusCodes) == 0 && !value.Clearance.Enabled && value.Clearance.Mode == "" && value.Clearance.CFCookies == "" && value.Clearance.CFClearance == "" && value.Clearance.UserAgent == "" && value.Clearance.Browser == "" && value.Clearance.FlareSolverrURL == "" && value.Clearance.TimeoutSec == 0 && value.Clearance.RefreshInterval == 0 && !value.Clearance.WarmUpOnStart
}

func normalizeProxyRuntime(value ProxyRuntime, legacyProxy string) ProxyRuntime {
	legacyProxy = strings.TrimSpace(legacyProxy)
	value.EgressMode = strings.ToLower(strings.TrimSpace(value.EgressMode))
	if value.EgressMode != "single_proxy" {
		value.EgressMode = "direct"
	}
	value.ProxyURL = strings.TrimSpace(value.ProxyURL)
	value.ResourceProxyURL = strings.TrimSpace(value.ResourceProxyURL)
	if legacyProxy != "" && value.ProxyURL == "" {
		// Keep the original global proxy setting functional after migrating to ProxyRuntime.
		value.Enabled = true
		value.EgressMode = "single_proxy"
		value.ProxyURL = legacyProxy
	}
	if value.EgressMode == "single_proxy" && value.ProxyURL == "" {
		value.EgressMode = "direct"
	}
	if len(value.ResetSessionStatusCodes) == 0 {
		value.ResetSessionStatusCodes = []int{403}
	}
	value.Clearance.Mode = strings.ToLower(strings.TrimSpace(value.Clearance.Mode))
	if value.Clearance.Mode != "manual" && value.Clearance.Mode != "flaresolverr" {
		value.Clearance.Mode = "none"
	}
	value.Clearance.CFCookies = strings.TrimSpace(value.Clearance.CFCookies)
	value.Clearance.CFClearance = strings.TrimSpace(value.Clearance.CFClearance)
	value.Clearance.UserAgent = strings.TrimSpace(value.Clearance.UserAgent)
	value.Clearance.FlareSolverrURL = strings.TrimRight(strings.TrimSpace(value.Clearance.FlareSolverrURL), "/")
	if value.Clearance.TimeoutSec <= 0 {
		value.Clearance.TimeoutSec = 60
	}
	if value.Clearance.RefreshInterval < 0 {
		value.Clearance.RefreshInterval = 0
	}
	return value
}
