package openaiweb

import (
	"bytes"
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

func buildProofToken(seed, difficulty, userAgent string, scripts []string, dataBuild string) (string, error) {
	cfg := buildPOWConfig(userAgent, scripts, dataBuild)
	answer, ok, err := powGenerate(seed, difficulty, cfg, 500000)
	if err != nil {
		return "", err
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
	target, err := hex.DecodeString(difficulty)
	if err != nil {
		return "", false, err
	}
	diffLen := len(difficulty) / 2
	seedBytes := []byte(seed)
	for i := 0; i < limit; i++ {
		clone := append([]any(nil), cfg...)
		clone[3] = i
		clone[9] = i >> 1
		raw, _ := json.Marshal(clone)
		encodedBytes := []byte(base64.StdEncoding.EncodeToString(raw))
		digest := sha3.Sum512(append(seedBytes, encodedBytes...))
		if bytes.Compare(digest[:diffLen], target) <= 0 {
			return string(encodedBytes), true, nil
		}
	}
	return "", false, nil
}
