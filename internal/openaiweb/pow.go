package openaiweb

import (
	"bytes"
	"context"
	"crypto/sha3"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"time"
)

const defaultPOWScript = "https://chatgpt.com/backend-api/sentinel/sdk.js"

const (
	powMaxAttempts         = 500000
	powCancelCheckInterval = 1024 // must remain a power of two
	powSolveTimeout        = 15 * time.Second
	powDigestSize          = 64
)

var scriptSrcRE = regexp.MustCompile(`<script[^>]+src=["']([^"']+)["']`)
var dataBuildRE = regexp.MustCompile(`data-build=["']([^"']+)["']`)
var cBuildRE = regexp.MustCompile(`c/[^/]*/_`)

func parsePOWResources(html string) ([]string, string) {
	scripts := []string{}
	dataBuild := ""
	for _, m := range scriptSrcRE.FindAllStringSubmatch(html, -1) {
		scripts = append(scripts, m[1])
		if dataBuild == "" {
			if b := cBuildRE.FindString(m[1]); b != "" {
				dataBuild = b
			}
		}
	}
	if len(scripts) == 0 {
		scripts = []string{defaultPOWScript}
	}
	if dataBuild == "" {
		if m := dataBuildRE.FindStringSubmatch(html); len(m) == 2 {
			dataBuild = m[1]
		}
	}
	return scripts, dataBuild
}

func buildLegacyRequirementsToken(userAgent string, scripts []string, dataBuild string) string {
	cfg := buildPOWConfig(userAgent, scripts, dataBuild)
	raw, _ := json.Marshal(cfg)
	return "gAAAAAC" + base64.StdEncoding.EncodeToString(raw)
}

func buildProofToken(ctx context.Context, seed, difficulty, userAgent string, scripts []string, dataBuild string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("proof token generation canceled: %w", err)
	}
	cfg := buildPOWConfig(userAgent, scripts, dataBuild)
	// The regular request deadline remains authoritative. This shorter local
	// budget prevents a pathological challenge from monopolizing a worker.
	solveCtx, cancel := context.WithTimeout(ctx, powSolveTimeout)
	defer cancel()
	answer, ok, err := powGenerateContext(solveCtx, seed, difficulty, cfg, powMaxAttempts)
	if err != nil {
		return "", fmt.Errorf("solve proof token: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("failed to solve proof token: difficulty=%s", difficulty)
	}
	return "gAAAAAB" + answer, nil
}

func buildPOWConfig(userAgent string, scripts []string, dataBuild string) []any {
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	script := defaultPOWScript
	if len(scripts) > 0 {
		script = scripts[rand.Intn(len(scripts))]
	}
	now := time.Now().In(time.FixedZone("EST", -5*3600)).Format("Mon Jan 02 2006 15:04:05") + " GMT-0500 (Eastern Standard Time)"
	return []any{3000, now, 4294705152, 1, userAgent, script, dataBuild, "en-US", "en-US,es-US,en,es", rand.Float64(), "vendor−Google Inc.", "location", "document", float64(time.Now().UnixNano()) / 1e6, newUUID(), "", 16, float64(time.Now().UnixNano()) / 1e6, 0, 0, 0, 0, 0, 0, 0}
}

func powGenerate(seed, difficulty string, cfg []any, limit int) (string, bool, error) {
	return powGenerateContext(context.Background(), seed, difficulty, cfg, limit)
}

func powGenerateContext(ctx context.Context, seed, difficulty string, cfg []any, limit int) (string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	target, err := hex.DecodeString(difficulty)
	if err != nil {
		return "", false, err
	}
	diffLen := len(difficulty) / 2
	if diffLen > powDigestSize {
		return "", false, fmt.Errorf("proof token difficulty is longer than SHA3-512 output")
	}
	if len(cfg) <= 9 {
		return "", false, fmt.Errorf("proof token configuration is incomplete")
	}
	// Keep callers' configuration immutable, but avoid a new slice for every
	// candidate while hashing a proof-of-work challenge.
	cfg = append([]any(nil), cfg...)
	seedBytes := []byte(seed)
	hasher := sha3.New512()
	var digest [powDigestSize]byte
	var encoded []byte
	for i := 0; i < limit; i++ {
		if i&(powCancelCheckInterval-1) == 0 {
			if err := ctx.Err(); err != nil {
				return "", false, err
			}
		}
		cfg[3] = i
		cfg[9] = i >> 1
		raw, err := json.Marshal(cfg)
		if err != nil {
			return "", false, err
		}
		encoded = base64.StdEncoding.AppendEncode(encoded[:0], raw)
		hasher.Reset()
		_, _ = hasher.Write(seedBytes)
		_, _ = hasher.Write(encoded)
		sum := hasher.Sum(digest[:0])
		if bytes.Compare(sum[:diffLen], target) <= 0 {
			return string(encoded), true, nil
		}
	}
	return "", false, nil
}
