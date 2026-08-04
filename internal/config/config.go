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
	AppName                             string       `json:"app_name"`
	ListenAddr                          string       `json:"listen_addr"`
	Timezone                            string       `json:"timezone"`
	StorageBackend                      string       `json:"storage_backend"`
	DatabaseURL                         string       `json:"database_url"`
	APIKeys                             []string     `json:"api_keys"`
	AuthKeyFile                         string       `json:"auth_key_file"`
	AccountFile                         string       `json:"account_file"`
	CallLogFile                         string       `json:"call_log_file"`
	ImageTagsFile                       string       `json:"image_tags_file"`
	RegisterFile                        string       `json:"register_file"`
	WebDistDir                          string       `json:"web_dist_dir"`
	ImageOutputDir                      string       `json:"image_output_dir"`
	ImageRetentionDays                  int          `json:"image_retention_days"`
	ChatGPTBaseURL                      string       `json:"chatgpt_base_url"`
	UpstreamTransport                   string       `json:"upstream_transport"`
	ImageWebModelSlug                   string       `json:"image_web_model_slug"`
	ImagePollTimeoutSecs                float64      `json:"image_poll_timeout_secs"`
	ImagePollIntervalSecs               float64      `json:"image_poll_interval_secs"`
	ImagePollInitialWaitSecs            float64      `json:"image_poll_initial_wait_secs"`
	ImageTaskTimeoutSecs                float64      `json:"image_task_timeout_secs"`
	ImageSettleSecs                     float64      `json:"image_settle_secs"`
	ImageCapacityBurstParallel          int          `json:"image_capacity_burst_parallel"`
	ImagePoolAutoRegisterEnabled        bool         `json:"image_pool_auto_register_enabled"`
	ImagePoolMinUsableAccounts          int          `json:"image_pool_min_usable_accounts"`
	ImagePoolIdleFloorAccounts          int          `json:"image_pool_idle_floor_accounts"`
	ImagePoolMaxUsableAccounts          int          `json:"image_pool_max_usable_accounts"`
	ImagePoolQuietAfterMinutes          int          `json:"image_pool_quiet_after_minutes"`
	ImagePoolRegisterCooldownMinutes    int          `json:"image_pool_register_cooldown_minutes"`
	ImagePoolMaxRegisterPerCycle        int          `json:"image_pool_max_register_per_cycle"`
	ImagePoolAutoRegisterIntervalSecs   int          `json:"image_pool_auto_register_interval_secs"`
	ImageGlobalMaxInflight              int          `json:"image_global_max_inflight"`
	ImagePrepareParallel                int          `json:"image_prepare_parallel"`
	ImageSubmitParallel                 int          `json:"image_submit_parallel"`
	ImagePollParallel                   int          `json:"image_poll_parallel"`
	ImageDownloadParallel               int          `json:"image_download_parallel"`
	ImageUploadParallel                 int          `json:"image_upload_parallel"`
	ImageAccountMaxInflightPerAccount   int          `json:"image_account_max_inflight_per_account"`
	ImageStallTimeoutSecs               float64      `json:"image_stall_timeout_secs"`
	ImageMaxSwitchesPerTask             int          `json:"image_max_switches_per_task"`
	ImageAccountPrecheckIntervalMinutes int          `json:"image_account_precheck_interval_minutes"`
	ImageAccountPrecheckConcurrency     int          `json:"image_account_precheck_concurrency"`
	ImageAccountPrecheckTimeoutSecs     float64      `json:"image_account_precheck_timeout_secs"`
	ImageCheckBeforeHitEnabled          bool         `json:"image_check_before_hit_enabled"`
	ImageSettleEnabled                  bool         `json:"image_settle_enabled"`
	ImageRestorationEnabled             bool         `json:"image_restoration_enabled"`
	ImagePostprocessWorker              string       `json:"image_postprocess_worker"`
	ImageRestorationModel               string       `json:"image_restoration_model"`
	ImagePostprocessTimeoutSecs         float64      `json:"image_postprocess_timeout_secs"`
	MaxImageAttempts                    int          `json:"max_image_attempts"`
	RequestTimeoutSecs                  float64      `json:"request_timeout_secs"`
	SearchModel                         string       `json:"search_model"`
	SearchTimeoutSecs                   float64      `json:"search_timeout_secs"`
	SearchPollIntervalSecs              float64      `json:"search_poll_interval_secs"`
	RefreshAccountIntervalMinutes       int          `json:"refresh_account_interval_minute"`
	RefreshAccountConcurrency           int          `json:"refresh_account_concurrency"`
	Proxy                               string       `json:"proxy"`
	ProxyRuntime                        ProxyRuntime `json:"proxy_runtime"`
	Models                              []string     `json:"models"`
	sourcePath                          string
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
		ImageRetentionDays:                  30,
		ChatGPTBaseURL:                      "https://chatgpt.com",
		UpstreamTransport:                   "standard",
		ImageWebModelSlug:                   "gpt-5-5",
		ImagePollTimeoutSecs:                600,
		ImagePollIntervalSecs:               3,
		ImagePollInitialWaitSecs:            0,
		ImageTaskTimeoutSecs:                630,
		ImageSettleSecs:                     2,
		ImageCapacityBurstParallel:          50,
		ImagePoolAutoRegisterEnabled:        false,
		ImagePoolMinUsableAccounts:          0,
		ImagePoolIdleFloorAccounts:          0,
		ImagePoolMaxUsableAccounts:          200,
		ImagePoolQuietAfterMinutes:          15,
		ImagePoolRegisterCooldownMinutes:    1,
		ImagePoolMaxRegisterPerCycle:        10,
		ImagePoolAutoRegisterIntervalSecs:   30,
		ImageGlobalMaxInflight:              120,
		ImagePrepareParallel:                20,
		ImageSubmitParallel:                 20,
		ImagePollParallel:                   80,
		ImageDownloadParallel:               20,
		ImageUploadParallel:                 12,
		ImageAccountMaxInflightPerAccount:   1,
		ImageStallTimeoutSecs:               150,
		ImageMaxSwitchesPerTask:             2,
		ImageAccountPrecheckIntervalMinutes: 10,
		ImageAccountPrecheckConcurrency:     6,
		ImageAccountPrecheckTimeoutSecs:     75,
		ImageCheckBeforeHitEnabled:          true,
		ImageSettleEnabled:                  true,
		ImageRestorationEnabled:             false,
		ImagePostprocessWorker:              "../postprocess/worker.mjs",
		ImageRestorationModel:               "../postprocess/models/scunet-color-real-gan.onnx",
		ImagePostprocessTimeoutSecs:         180,
		MaxImageAttempts:                    3,
		RequestTimeoutSecs:                  120,
		SearchModel:                         "gpt-5-5",
		SearchTimeoutSecs:                   300,
		SearchPollIntervalSecs:              3,
		RefreshAccountIntervalMinutes:       60,
		RefreshAccountConcurrency:           8,
		ProxyRuntime:                        ProxyRuntime{Enabled: true, EgressMode: "direct", ResetSessionStatusCodes: []int{403}, Clearance: ClearanceRuntime{Enabled: false, Mode: "none", Browser: "chrome", TimeoutSec: 60, RefreshInterval: 3600}},
		Models:                              []string{"gpt-image-2"},
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
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return cfg, fmt.Errorf("resolve config directory: %w", err)
	}
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
	if !filepath.IsAbs(cfg.ImagePostprocessWorker) {
		cfg.ImagePostprocessWorker = resolveBundledPath(base, cfg.ImagePostprocessWorker)
	}
	if !filepath.IsAbs(cfg.ImageRestorationModel) {
		cfg.ImageRestorationModel = resolveBundledPath(base, cfg.ImageRestorationModel)
	}
	cfg = cfg.Normalize()
	cfg.sourcePath = filepath.Clean(path)
	return cfg, nil
}

func resolveBundledPath(configDir, value string) string {
	configured := filepath.Clean(filepath.Join(configDir, value))
	if _, err := os.Stat(configured); err == nil {
		return configured
	}
	// Older settings saves persisted paths relative to the application root
	// after they had already been resolved from the configs directory.
	applicationRelative := filepath.Clean(filepath.Join(filepath.Dir(configDir), value))
	if _, err := os.Stat(applicationRelative); err == nil {
		return applicationRelative
	}
	return configured
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
	if c.ImageRetentionDays <= 0 {
		c.ImageRetentionDays = d.ImageRetentionDays
	}
	if c.ImageRetentionDays > 3650 {
		c.ImageRetentionDays = 3650
	}
	if strings.TrimSpace(c.ChatGPTBaseURL) == "" {
		c.ChatGPTBaseURL = d.ChatGPTBaseURL
	}
	c.ChatGPTBaseURL = strings.TrimRight(strings.TrimSpace(c.ChatGPTBaseURL), "/")
	if strings.TrimSpace(c.ImageWebModelSlug) == "" {
		c.ImageWebModelSlug = d.ImageWebModelSlug
	}
	if c.ImagePollTimeoutSecs < d.ImagePollTimeoutSecs {
		c.ImagePollTimeoutSecs = d.ImagePollTimeoutSecs
	}
	if c.ImagePollTimeoutSecs > d.ImagePollTimeoutSecs {
		c.ImagePollTimeoutSecs = d.ImagePollTimeoutSecs
	}
	if c.ImagePollIntervalSecs <= 0 {
		c.ImagePollIntervalSecs = d.ImagePollIntervalSecs
	}
	if c.ImagePollInitialWaitSecs < 0 {
		c.ImagePollInitialWaitSecs = 0
	}
	// The request budget must leave room for the bounded preparation phase plus
	// a full 600-second submitted generation. Older configurations, including
	// zero (unlimited), are all migrated to this fixed end-to-end budget.
	c.ImageTaskTimeoutSecs = d.ImageTaskTimeoutSecs
	if c.ImageSettleSecs < 0 {
		c.ImageSettleSecs = 0
	}
	if c.ImageCapacityBurstParallel <= 0 {
		c.ImageCapacityBurstParallel = d.ImageCapacityBurstParallel
	}
	if c.ImageCapacityBurstParallel > 10000 {
		c.ImageCapacityBurstParallel = 10000
	}
	if c.ImagePoolMinUsableAccounts < 0 {
		c.ImagePoolMinUsableAccounts = 0
	}
	if c.ImagePoolIdleFloorAccounts < 0 {
		c.ImagePoolIdleFloorAccounts = 0
	}
	if c.ImagePoolMaxUsableAccounts <= 0 {
		c.ImagePoolMaxUsableAccounts = d.ImagePoolMaxUsableAccounts
	}
	if c.ImagePoolMaxUsableAccounts > 10000 {
		c.ImagePoolMaxUsableAccounts = 10000
	}
	if c.ImagePoolMinUsableAccounts > c.ImagePoolMaxUsableAccounts {
		c.ImagePoolMinUsableAccounts = c.ImagePoolMaxUsableAccounts
	}
	if c.ImagePoolIdleFloorAccounts > c.ImagePoolMaxUsableAccounts {
		c.ImagePoolIdleFloorAccounts = c.ImagePoolMaxUsableAccounts
	}
	if c.ImagePoolQuietAfterMinutes <= 0 {
		c.ImagePoolQuietAfterMinutes = d.ImagePoolQuietAfterMinutes
	}
	if c.ImagePoolQuietAfterMinutes > 1440 {
		c.ImagePoolQuietAfterMinutes = 1440
	}
	if c.ImagePoolRegisterCooldownMinutes <= 0 {
		c.ImagePoolRegisterCooldownMinutes = d.ImagePoolRegisterCooldownMinutes
	}
	if c.ImagePoolRegisterCooldownMinutes > 1440 {
		c.ImagePoolRegisterCooldownMinutes = 1440
	}
	if c.ImagePoolMaxRegisterPerCycle <= 0 {
		c.ImagePoolMaxRegisterPerCycle = d.ImagePoolMaxRegisterPerCycle
	}
	if c.ImagePoolMaxRegisterPerCycle > 1000 {
		c.ImagePoolMaxRegisterPerCycle = 1000
	}
	if c.ImagePoolAutoRegisterIntervalSecs <= 0 {
		c.ImagePoolAutoRegisterIntervalSecs = d.ImagePoolAutoRegisterIntervalSecs
	}
	if c.ImagePoolAutoRegisterIntervalSecs < 5 {
		c.ImagePoolAutoRegisterIntervalSecs = 5
	}
	if c.ImagePoolAutoRegisterIntervalSecs > 3600 {
		c.ImagePoolAutoRegisterIntervalSecs = 3600
	}
	if c.ImageGlobalMaxInflight <= 0 {
		c.ImageGlobalMaxInflight = d.ImageGlobalMaxInflight
	}
	if c.ImageGlobalMaxInflight > 10000 {
		c.ImageGlobalMaxInflight = 10000
	}
	c.ImagePrepareParallel = normalizePositiveParallel(c.ImagePrepareParallel, d.ImagePrepareParallel, 1000)
	c.ImageSubmitParallel = normalizePositiveParallel(c.ImageSubmitParallel, d.ImageSubmitParallel, 1000)
	c.ImagePollParallel = normalizePositiveParallel(c.ImagePollParallel, d.ImagePollParallel, 5000)
	c.ImageDownloadParallel = normalizePositiveParallel(c.ImageDownloadParallel, d.ImageDownloadParallel, 1000)
	c.ImageUploadParallel = normalizePositiveParallel(c.ImageUploadParallel, d.ImageUploadParallel, 1000)
	if c.ImageAccountMaxInflightPerAccount <= 0 {
		c.ImageAccountMaxInflightPerAccount = d.ImageAccountMaxInflightPerAccount
	}
	if c.ImageAccountMaxInflightPerAccount > 20 {
		c.ImageAccountMaxInflightPerAccount = 20
	}
	if c.ImageStallTimeoutSecs <= 0 {
		c.ImageStallTimeoutSecs = d.ImageStallTimeoutSecs
	}
	if c.ImageStallTimeoutSecs < 15 {
		c.ImageStallTimeoutSecs = 15
	}
	if c.ImageStallTimeoutSecs >= c.ImagePollTimeoutSecs {
		c.ImageStallTimeoutSecs = maxFloat(15, c.ImagePollTimeoutSecs-1)
	}
	if c.ImageMaxSwitchesPerTask <= 0 {
		c.ImageMaxSwitchesPerTask = d.ImageMaxSwitchesPerTask
	}
	if c.ImageMaxSwitchesPerTask > 5 {
		c.ImageMaxSwitchesPerTask = 5
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
	if strings.TrimSpace(c.ImagePostprocessWorker) == "" {
		c.ImagePostprocessWorker = d.ImagePostprocessWorker
	}
	if strings.TrimSpace(c.ImageRestorationModel) == "" {
		c.ImageRestorationModel = d.ImageRestorationModel
	}
	if c.ImagePostprocessTimeoutSecs <= 0 {
		c.ImagePostprocessTimeoutSecs = d.ImagePostprocessTimeoutSecs
	}
	if c.ImagePostprocessTimeoutSecs > 1800 {
		c.ImagePostprocessTimeoutSecs = 1800
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
	if c.RefreshAccountIntervalMinutes <= 0 {
		c.RefreshAccountIntervalMinutes = d.RefreshAccountIntervalMinutes
	}
	if c.RefreshAccountIntervalMinutes > 525600 {
		c.RefreshAccountIntervalMinutes = 525600
	}
	if c.RefreshAccountConcurrency <= 0 {
		c.RefreshAccountConcurrency = d.RefreshAccountConcurrency
	}
	c.Proxy = strings.TrimSpace(c.Proxy)
	if proxyRuntimeEmpty(c.ProxyRuntime) {
		c.ProxyRuntime = d.ProxyRuntime
	}
	c.ProxyRuntime = normalizeProxyRuntime(c.ProxyRuntime, c.Proxy)
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

func normalizePositiveParallel(value, fallback, maximum int) int {
	if value <= 0 {
		value = fallback
	}
	if value > maximum {
		value = maximum
	}
	return value
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
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
