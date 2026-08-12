package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultNormalize(t *testing.T) {
	cfg := Config{}.Normalize()
	if cfg.AppName != "IMAGE POOL" {
		t.Fatalf("app=%q", cfg.AppName)
	}
	if cfg.ImageWebModelSlug != "gpt-5-5" {
		t.Fatalf("slug=%q", cfg.ImageWebModelSlug)
	}
	if cfg.ImageRetentionDays != 30 {
		t.Fatalf("image retention days=%d", cfg.ImageRetentionDays)
	}
	if cfg.ImageAccountPrecheckIntervalMinutes != 10 {
		t.Fatalf("precheck interval=%d", cfg.ImageAccountPrecheckIntervalMinutes)
	}
	if cfg.ImageAccountPrecheckConcurrency != 6 || cfg.ImageAccountPrecheckTimeoutSecs != 75 {
		t.Fatalf("precheck limits=%d/%.0f", cfg.ImageAccountPrecheckConcurrency, cfg.ImageAccountPrecheckTimeoutSecs)
	}
	if cfg.ImagePollTimeoutSecs != 600 {
		t.Fatalf("image poll timeout=%.0f", cfg.ImagePollTimeoutSecs)
	}
	if cfg.ImageTaskTimeoutSecs != 630 {
		t.Fatalf("image task timeout=%.0f", cfg.ImageTaskTimeoutSecs)
	}
	if cfg.ImageAccountMaxInflightPerAccount != 1 {
		t.Fatalf("image account max inflight=%d", cfg.ImageAccountMaxInflightPerAccount)
	}
	if !Default().Normalize().ImageAccountDynamicSlots {
		t.Fatal("image account dynamic slots should default to enabled")
	}
	if cfg.ProxyRuntime.Clearance.FlareSolverrURL != "http://flaresolverr:8191" {
		t.Fatalf("flaresolverr url=%q", cfg.ProxyRuntime.Clearance.FlareSolverrURL)
	}
	if cfg.ImageGlobalMaxInflight != 120 || cfg.ImagePrepareParallel != 20 || cfg.ImageSubmitParallel != 20 || cfg.ImagePollParallel != 80 || cfg.ImageDownloadParallel != 20 || cfg.ImageUploadParallel != 12 {
		t.Fatalf("image concurrency=%d/%d/%d/%d/%d/%d", cfg.ImageGlobalMaxInflight, cfg.ImagePrepareParallel, cfg.ImageSubmitParallel, cfg.ImagePollParallel, cfg.ImageDownloadParallel, cfg.ImageUploadParallel)
	}
	if cfg.ImagePoolAutoRegisterEnabled || cfg.ImagePoolMinUsableAccounts != 0 || cfg.ImagePoolIdleFloorAccounts != 0 || cfg.ImagePoolMaxUsableAccounts != 200 || cfg.ImagePoolQuietAfterMinutes != 15 || cfg.ImagePoolRegisterCooldownMinutes != 1 || cfg.ImagePoolMaxRegisterPerCycle != 10 || cfg.ImagePoolAutoRegisterIntervalSecs != 30 {
		t.Fatalf("auto registration defaults=%#v", cfg)
	}
	if cfg.ImageStallTimeoutSecs != 150 || cfg.ImageMaxSwitchesPerTask != 2 {
		t.Fatalf("image switching=%.0f/%d", cfg.ImageStallTimeoutSecs, cfg.ImageMaxSwitchesPerTask)
	}
	if cfg.RefreshAccountIntervalMinutes != 60 {
		t.Fatalf("refresh interval=%d", cfg.RefreshAccountIntervalMinutes)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "dev-key" {
		t.Fatalf("keys=%#v", cfg.APIKeys)
	}
}

func TestNormalizeCapsImagePoolAutoRegistration(t *testing.T) {
	cfg := (Config{
		ImagePoolAutoRegisterEnabled:      true,
		ImagePoolMinUsableAccounts:        -1,
		ImagePoolIdleFloorAccounts:        500,
		ImagePoolMaxUsableAccounts:        2,
		ImagePoolQuietAfterMinutes:        5000,
		ImagePoolRegisterCooldownMinutes:  -1,
		ImagePoolMaxRegisterPerCycle:      5000,
		ImagePoolAutoRegisterIntervalSecs: 1,
	}).Normalize()
	if !cfg.ImagePoolAutoRegisterEnabled || cfg.ImagePoolMinUsableAccounts != 0 || cfg.ImagePoolIdleFloorAccounts != 2 || cfg.ImagePoolMaxUsableAccounts != 2 || cfg.ImagePoolQuietAfterMinutes != 1440 || cfg.ImagePoolRegisterCooldownMinutes != 1 || cfg.ImagePoolMaxRegisterPerCycle != 1000 || cfg.ImagePoolAutoRegisterIntervalSecs != 5 {
		t.Fatalf("auto registration normalization=%#v", cfg)
	}
}

func TestNormalizeFixesImageWaits(t *testing.T) {
	cfg := Config{ImagePollTimeoutSecs: 300, ImageTaskTimeoutSecs: 600}.Normalize()
	if cfg.ImagePollTimeoutSecs != 600 || cfg.ImageTaskTimeoutSecs != 630 {
		t.Fatalf("image timeouts=%.0f/%.0f", cfg.ImagePollTimeoutSecs, cfg.ImageTaskTimeoutSecs)
	}
	if timeout := (Config{ImageTaskTimeoutSecs: 120}).Normalize().ImageTaskTimeoutSecs; timeout != 630 {
		t.Fatalf("configured image task timeout=%.0f", timeout)
	}
}

func TestNormalizeMigratesLegacyImagePollTimeout(t *testing.T) {
	for _, configured := range []float64{60, 90, 180} {
		if timeout := (Config{ImagePollTimeoutSecs: configured}).Normalize().ImagePollTimeoutSecs; timeout != 600 {
			t.Fatalf("configured=%.0f image poll timeout=%.0f", configured, timeout)
		}
	}
}

func TestZeroImageTaskTimeoutMigratesToBoundedDefault(t *testing.T) {
	if timeout := (Config{ImageTaskTimeoutSecs: 0}).Normalize().ImageTaskTimeoutSecs; timeout != 630 {
		t.Fatalf("image task timeout=%.0f", timeout)
	}
}

func TestNormalizePreservesImageRetentionDays(t *testing.T) {
	if days := (Config{ImageRetentionDays: 7}).Normalize().ImageRetentionDays; days != 7 {
		t.Fatalf("image retention days=%d", days)
	}
	if days := (Config{ImageRetentionDays: 0}).Normalize().ImageRetentionDays; days != 30 {
		t.Fatalf("default image retention days=%d", days)
	}
	if days := (Config{ImageRetentionDays: 5000}).Normalize().ImageRetentionDays; days != 3650 {
		t.Fatalf("capped image retention days=%d", days)
	}
}

func TestNormalizeCapsImageAccountMaxInflight(t *testing.T) {
	if got := (Config{ImageAccountMaxInflightPerAccount: 3}).Normalize().ImageAccountMaxInflightPerAccount; got != 3 {
		t.Fatalf("max inflight=%d", got)
	}
	if got := (Config{ImageAccountMaxInflightPerAccount: 0}).Normalize().ImageAccountMaxInflightPerAccount; got != 1 {
		t.Fatalf("default max inflight=%d", got)
	}
	if got := (Config{ImageAccountMaxInflightPerAccount: 99}).Normalize().ImageAccountMaxInflightPerAccount; got != 20 {
		t.Fatalf("capped max inflight=%d", got)
	}
}

func TestImageAccountDynamicSlotsCanBeDisabled(t *testing.T) {
	if (Config{ImageAccountDynamicSlots: false}).Normalize().ImageAccountDynamicSlots {
		t.Fatal("static image account slots were normalized back to dynamic")
	}
}

func TestNormalizeCapsImageConcurrency(t *testing.T) {
	cfg := (Config{
		ImageGlobalMaxInflight:  20000,
		ImagePrepareParallel:    0,
		ImageSubmitParallel:     20000,
		ImagePollParallel:       20000,
		ImageDownloadParallel:   -1,
		ImageUploadParallel:     20000,
		ImageStallTimeoutSecs:   900,
		ImageMaxSwitchesPerTask: 20,
	}).Normalize()
	if cfg.ImageGlobalMaxInflight != 10000 || cfg.ImagePrepareParallel != 20 || cfg.ImageSubmitParallel != 1000 || cfg.ImagePollParallel != 5000 || cfg.ImageDownloadParallel != 20 || cfg.ImageUploadParallel != 1000 {
		t.Fatalf("concurrency=%#v", cfg)
	}
	if cfg.ImageStallTimeoutSecs != 599 || cfg.ImageMaxSwitchesPerTask != 5 {
		t.Fatalf("switching=%.0f/%d", cfg.ImageStallTimeoutSecs, cfg.ImageMaxSwitchesPerTask)
	}
}

func TestNormalizeMigratesGlobalProxyToRuntime(t *testing.T) {
	cfg := Config{Proxy: "http://127.0.0.1:7890", ProxyRuntime: ProxyRuntime{Enabled: true, EgressMode: "direct"}}.Normalize()
	if cfg.ProxyRuntime.EgressMode != "single_proxy" || cfg.ProxyRuntime.ProxyURL != cfg.Proxy || !cfg.ProxyRuntime.Enabled {
		t.Fatalf("proxy runtime=%#v", cfg.ProxyRuntime)
	}
}

func TestLoadMergesAndMakesPathsRelativeToConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	err := os.WriteFile(path, []byte(`{"listen_addr":":9090","api_keys":[" a ","a","b"],"auth_key_file":"auth.json","account_file":"accounts.json","image_output_dir":"images","image_web_model_slug":"gpt-5-3","image_poll_interval_secs":0}`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9090" || cfg.ImageWebModelSlug != "gpt-5-3" {
		t.Fatalf("bad cfg: %#v", cfg)
	}
	if cfg.AccountFile != filepath.Join(dir, "accounts.json") {
		t.Fatalf("account path=%s", cfg.AccountFile)
	}
	if cfg.AuthKeyFile != filepath.Join(dir, "auth.json") {
		t.Fatalf("auth key path=%s", cfg.AuthKeyFile)
	}
	if cfg.ImagePollIntervalSecs <= 0 {
		t.Fatal("interval not normalized")
	}
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("keys=%#v", cfg.APIKeys)
	}
}

func TestSaveWritesUpdatedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := LoadIfExists(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ImageWebModelSlug = "gpt-5-6"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ImageWebModelSlug != "gpt-5-6" || reloaded.SourcePath() != path {
		t.Fatalf("config=%#v", reloaded)
	}
}
