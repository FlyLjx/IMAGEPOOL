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
	if cfg.ImageAccountPrecheckIntervalMinutes != 10 {
		t.Fatalf("precheck interval=%d", cfg.ImageAccountPrecheckIntervalMinutes)
	}
	if cfg.ImageAccountPrecheckConcurrency != 6 || cfg.ImageAccountPrecheckTimeoutSecs != 75 {
		t.Fatalf("precheck limits=%d/%.0f", cfg.ImageAccountPrecheckConcurrency, cfg.ImageAccountPrecheckTimeoutSecs)
	}
	if cfg.ImagePollTimeoutSecs != 180 {
		t.Fatalf("image poll timeout=%.0f", cfg.ImagePollTimeoutSecs)
	}
	if cfg.ImageTaskTimeoutSecs != 0 {
		t.Fatalf("image task timeout=%.0f", cfg.ImageTaskTimeoutSecs)
	}
	if cfg.RefreshAccountIntervalMinutes != 60 {
		t.Fatalf("refresh interval=%d", cfg.RefreshAccountIntervalMinutes)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "dev-key" {
		t.Fatalf("keys=%#v", cfg.APIKeys)
	}
}

func TestNormalizeCapsImageWaits(t *testing.T) {
	cfg := Config{ImagePollTimeoutSecs: 300, ImageTaskTimeoutSecs: 600}.Normalize()
	if cfg.ImagePollTimeoutSecs != 180 || cfg.ImageTaskTimeoutSecs != 300 {
		t.Fatalf("image timeouts=%.0f/%.0f", cfg.ImagePollTimeoutSecs, cfg.ImageTaskTimeoutSecs)
	}
}

func TestNormalizeMigratesLegacyImagePollTimeout(t *testing.T) {
	if timeout := (Config{ImagePollTimeoutSecs: 60}).Normalize().ImagePollTimeoutSecs; timeout != 180 {
		t.Fatalf("image poll timeout=%.0f", timeout)
	}
}

func TestZeroImageTaskTimeoutDisablesTotalDeadline(t *testing.T) {
	if timeout := (Config{ImageTaskTimeoutSecs: 0}).Normalize().ImageTaskTimeoutSecs; timeout != 0 {
		t.Fatalf("image task timeout=%.0f", timeout)
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
