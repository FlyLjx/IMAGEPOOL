package openaiweb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildProofTokenHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := buildProofToken(ctx, "seed", "ff", defaultUserAgent, nil, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context canceled", err)
	}
}

func TestBuildProofTokenHonorsDeadlineDuringSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := buildProofToken(ctx, "seed", strings.Repeat("00", powDigestSize), defaultUserAgent, nil, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline exceeded", err)
	}
}

func TestPowGenerateContextStopsDuringSearch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(time.Millisecond)
		cancel()
	}()

	_, ok, err := powGenerateContext(ctx, "seed", strings.Repeat("00", powDigestSize), buildPOWConfig(defaultUserAgent, nil, ""), powMaxAttempts)
	if ok {
		t.Fatal("unexpected proof token for an all-zero target")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context canceled", err)
	}
}

func TestPowGenerateDoesNotMutateConfig(t *testing.T) {
	cfg := []any{3000, "date", 4294705152, 1, defaultUserAgent, defaultPOWScript, "", "en-US", "en-US,es-US,en,es", 0.5}
	wantNonce, wantHalfNonce := cfg[3], cfg[9]
	token, ok, err := powGenerate("seed", "ff", cfg, 1)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	const want = "WzMwMDAsImRhdGUiLDQyOTQ3MDUxNTIsMCwiTW96aWxsYS81LjAgKFdpbmRvd3MgTlQgMTAuMDsgV2luNjQ7IHg2NCkgQXBwbGVXZWJLaXQvNTM3LjM2IChLSFRNTCwgbGlrZSBHZWNrbykgQ2hyb21lLzE0NC4wLjAuMCBTYWZhcmkvNTM3LjM2IiwiaHR0cHM6Ly9jaGF0Z3B0LmNvbS9iYWNrZW5kLWFwaS9zZW50aW5lbC9zZGsuanMiLCIiLCJlbi1VUyIsImVuLVVTLGVzLVVTLGVuLGVzIiwwXQ=="
	if token != want {
		t.Fatalf("proof config=%q want=%q", token, want)
	}
	if cfg[3] != wantNonce || cfg[9] != wantHalfNonce {
		t.Fatalf("config mutated: %#v", cfg)
	}
}
