package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	latestVersionLookupTimeout   = 8 * time.Second
	latestChangelogLookupTimeout = 8 * time.Second
)

var semverTagPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

var latestVersionLookupConfig = struct {
	client              *http.Client
	githubLatestRelease string
	githubTags          string
	jsdelivrResolved    string
	jsdelivrChangelog   string
	githubRawChangelog  string
}{
	client:              http.DefaultClient,
	githubLatestRelease: "https://api.github.com/repos/FlyLjx/IMAGEPOOL/releases/latest",
	githubTags:          "https://api.github.com/repos/FlyLjx/IMAGEPOOL/tags?per_page=30",
	jsdelivrResolved:    "https://data.jsdelivr.com/v1/packages/gh/FlyLjx/IMAGEPOOL/resolved?specifier=latest",
	jsdelivrChangelog:   "https://cdn.jsdelivr.net/gh/FlyLjx/IMAGEPOOL@v%s/CHANGELOG.md",
	githubRawChangelog:  "https://raw.githubusercontent.com/FlyLjx/IMAGEPOOL/v%s/CHANGELOG.md",
}

type latestVersionResponse struct {
	Version         string    `json:"version"`
	Source          string    `json:"source"`
	CheckedAt       time.Time `json:"checked_at"`
	Current         string    `json:"current,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	Changelog       string    `json:"changelog,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type latestVersionLookup struct {
	source  string
	version string
	err     error
}

func (s *Server) handleLatestVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	current := normalizeVersion(r.URL.Query().Get("current"))
	ctx, cancel := context.WithTimeout(r.Context(), latestVersionLookupTimeout)
	defer cancel()
	version, source, lookupErr := latestPublishedVersion(ctx, latestVersionLookupConfig.client)
	if version == "" {
		version = current
		source = "fallback"
	}
	response := latestVersionResponse{
		Version:         version,
		Source:          source,
		CheckedAt:       time.Now(),
		Current:         current,
		UpdateAvailable: isNewerSemver(version, current),
	}
	if lookupErr != nil && source == "fallback" {
		response.Error = lookupErr.Error()
	}
	if version != "" && source != "fallback" {
		if changelog := fetchLatestChangelog(r.Context(), latestVersionLookupConfig.client, version); changelog != "" {
			response.Changelog = changelog
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func latestPublishedVersion(ctx context.Context, client *http.Client) (string, string, error) {
	lookupFns := []func(context.Context, *http.Client) latestVersionLookup{
		lookupGitHubLatestRelease,
		lookupJSDelivrResolvedVersion,
		lookupGitHubTagsVersion,
	}
	results := make(chan latestVersionLookup, len(lookupFns))
	for _, fn := range lookupFns {
		go func(fn func(context.Context, *http.Client) latestVersionLookup) {
			results <- fn(ctx, client)
		}(fn)
	}
	var latest latestVersionLookup
	var messages []string
	for range lookupFns {
		select {
		case item := <-results:
			if item.err != nil {
				messages = append(messages, fmt.Sprintf("%s: %v", item.source, item.err))
				continue
			}
			item.version = normalizeVersion(item.version)
			if item.version == "" {
				continue
			}
			if latest.version == "" || isNewerSemver(item.version, latest.version) {
				latest = item
			}
		case <-ctx.Done():
			messages = append(messages, ctx.Err().Error())
		}
	}
	if latest.version != "" {
		return latest.version, latest.source, nil
	}
	if len(messages) == 0 {
		return "", "", fmt.Errorf("no published version found")
	}
	return "", "", fmt.Errorf("%s", strings.Join(messages, "; "))
}

func lookupGitHubLatestRelease(ctx context.Context, client *http.Client) latestVersionLookup {
	var payload struct {
		TagName string `json:"tag_name"`
	}
	err := fetchJSON(ctx, client, latestVersionLookupConfig.githubLatestRelease, &payload)
	if err != nil {
		return latestVersionLookup{source: "github_release", err: err}
	}
	return latestVersionLookup{source: "github_release", version: payload.TagName}
}

func lookupJSDelivrResolvedVersion(ctx context.Context, client *http.Client) latestVersionLookup {
	var payload struct {
		Version string `json:"version"`
	}
	err := fetchJSON(ctx, client, latestVersionLookupConfig.jsdelivrResolved, &payload)
	if err != nil {
		return latestVersionLookup{source: "jsdelivr", err: err}
	}
	return latestVersionLookup{source: "jsdelivr", version: payload.Version}
}

func lookupGitHubTagsVersion(ctx context.Context, client *http.Client) latestVersionLookup {
	var tags []struct {
		Name string `json:"name"`
	}
	err := fetchJSON(ctx, client, latestVersionLookupConfig.githubTags, &tags)
	if err != nil {
		return latestVersionLookup{source: "github_tags", err: err}
	}
	latest := ""
	for _, tag := range tags {
		version := normalizeVersion(tag.Name)
		if version == "" {
			continue
		}
		if latest == "" || isNewerSemver(version, latest) {
			latest = version
		}
	}
	return latestVersionLookup{source: "github_tags", version: latest}
}

func fetchLatestChangelog(parent context.Context, client *http.Client, version string) string {
	ctx, cancel := context.WithTimeout(parent, latestChangelogLookupTimeout)
	defer cancel()
	for _, format := range []string{latestVersionLookupConfig.jsdelivrChangelog, latestVersionLookupConfig.githubRawChangelog} {
		text, err := fetchText(ctx, client, fmt.Sprintf(format, version))
		if err == nil && strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func fetchJSON(ctx context.Context, client *http.Client, url string, output any) error {
	text, err := fetchText(ctx, client, url)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), output); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}
	return nil
}

func fetchText(ctx context.Context, client *http.Client, url string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "IMAGE-POOL-Version-Checker")
	request.Header.Set("Accept", "application/json,text/plain,*/*")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	match := semverTagPattern.FindStringSubmatch(value)
	if match == nil {
		return ""
	}
	return fmt.Sprintf("%s.%s.%s", match[1], match[2], match[3])
}

func isNewerSemver(candidate, current string) bool {
	left := semverParts(candidate)
	right := semverParts(current)
	if left == nil || right == nil {
		return false
	}
	for index := range left {
		if left[index] > right[index] {
			return true
		}
		if left[index] < right[index] {
			return false
		}
	}
	return false
}

func semverParts(value string) []int {
	match := semverTagPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return nil
	}
	parts := make([]int, 3)
	for i := 0; i < 3; i++ {
		for _, ch := range match[i+1] {
			parts[i] = parts[i]*10 + int(ch-'0')
		}
	}
	return parts
}
