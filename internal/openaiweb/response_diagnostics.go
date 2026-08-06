package openaiweb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ImageResponseSummary is a shape-only view of an upstream SSE or
// conversation snapshot. It deliberately keeps values out of logs: prompt
// text, access tokens, image URLs, and reference IDs must never be persisted
// as diagnostics.
type ImageResponseSummary struct {
	Bytes                 int
	Fingerprint           string
	Roles                 []string
	ToolMarkers           []string
	StatusMarkers         []string
	ReferenceMarkers      []string
	CandidateKeys         []string
	CandidateValueCount   int
	RawFileReferenceCount int
	RawSedimentCount      int
	FileReferenceCount    int
	SedimentCount         int
}

// ImageResponseDiagnostics stores the latest shape summary observed by one
// stream or polling attempt. SnapshotCount is cumulative for that stream or
// polling loop; the Last* fields describe the latest response only.
type ImageResponseDiagnostics struct {
	SnapshotCount         int
	LastBytes             int
	LastFingerprint       string
	LastRoles             []string
	LastToolMarkers       []string
	LastStatusMarkers     []string
	LastReferenceMarkers  []string
	LastCandidateKeys     []string
	LastCandidateValues   int
	LastRawFileReferences int
	LastRawSediments      int
	LastFileReferences    int
	LastSediments         int
}

func (d *ImageResponseDiagnostics) Observe(summary ImageResponseSummary) {
	if d == nil {
		return
	}
	d.SnapshotCount++
	d.LastBytes = summary.Bytes
	d.LastFingerprint = summary.Fingerprint
	d.LastRoles = append([]string(nil), summary.Roles...)
	d.LastToolMarkers = append([]string(nil), summary.ToolMarkers...)
	d.LastStatusMarkers = append([]string(nil), summary.StatusMarkers...)
	d.LastReferenceMarkers = append([]string(nil), summary.ReferenceMarkers...)
	d.LastCandidateKeys = append([]string(nil), summary.CandidateKeys...)
	d.LastCandidateValues = summary.CandidateValueCount
	d.LastRawFileReferences = summary.RawFileReferenceCount
	d.LastRawSediments = summary.RawSedimentCount
	d.LastFileReferences = summary.FileReferenceCount
	d.LastSediments = summary.SedimentCount
}

// LogFields returns a compact, value-free field list suitable for a single
// log line. All list members come from fixed classifications or JSON key
// names; arbitrary upstream strings are never returned.
func (d ImageResponseDiagnostics) LogFields() string {
	return fmt.Sprintf("response_snapshots=%d response_bytes=%d response_fingerprint=%s response_roles=%s response_tools=%s response_statuses=%s response_reference_markers=%s response_candidate_keys=%s response_candidate_values=%d response_raw_file_refs=%d response_raw_sediment_refs=%d response_file_refs=%d response_sediment_refs=%d",
		d.SnapshotCount,
		d.LastBytes,
		nonEmptyDiagnosticValue(d.LastFingerprint),
		joinDiagnosticValues(d.LastRoles),
		joinDiagnosticValues(d.LastToolMarkers),
		joinDiagnosticValues(d.LastStatusMarkers),
		joinDiagnosticValues(d.LastReferenceMarkers),
		joinDiagnosticValues(d.LastCandidateKeys),
		d.LastCandidateValues,
		d.LastRawFileReferences,
		d.LastRawSediments,
		d.LastFileReferences,
		d.LastSediments,
	)
}

func nonEmptyDiagnosticValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}

func joinDiagnosticValues(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ",")
}

func summarizeImageResponse(value any, rawBytes int, excludedFileIDs map[string]bool) ImageResponseSummary {
	encoded, _ := json.Marshal(value)
	if rawBytes <= 0 {
		rawBytes = len(encoded)
	}
	hash := sha256.Sum256(encoded)
	fileIDs, sedimentIDs := ExtractImageReferenceIDs(value)
	filteredFileIDs := filterExcludedIDs(fileIDs, excludedFileIDs)
	filteredSedimentIDs := filterExcludedIDs(sedimentIDs, excludedFileIDs)

	summary := ImageResponseSummary{
		Bytes:                 rawBytes,
		Fingerprint:           hex.EncodeToString(hash[:])[:16],
		RawFileReferenceCount: len(fileIDs),
		RawSedimentCount:      len(sedimentIDs),
		FileReferenceCount:    len(filteredFileIDs),
		SedimentCount:         len(filteredSedimentIDs),
	}
	roles := map[string]bool{}
	toolMarkers := map[string]bool{}
	statusMarkers := map[string]bool{}
	referenceMarkers := map[string]bool{}
	candidateKeys := map[string]bool{}
	var candidateValueCount int

	addRole := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return
		}
		switch value {
		case "assistant", "tool", "user", "system", "developer":
			roles[value] = true
		default:
			roles["other"] = true
		}
	}
	addStatus := func(value string) {
		value = normalizeImageGenerationStatus(value)
		if value == "" {
			return
		}
		switch {
		case value == "server_timeout" || value == "interrupted" || value == "failed" || value == "failure" || value == "error" || value == "server_error" || value == "generation_failed" || value == "image_generation_failed" || value == "cancelled" || value == "canceled" || value == "aborted":
			statusMarkers[value] = true
		case strings.Contains(value, "progress") || strings.Contains(value, "pending") || strings.Contains(value, "running") || strings.Contains(value, "queue") || strings.Contains(value, "generat"):
			statusMarkers["in_progress"] = true
		default:
			statusMarkers["other"] = true
		}
	}
	addReferenceKey := func(key string, value any) {
		lower := strings.ToLower(strings.TrimSpace(key))
		marker := ""
		switch lower {
		case "asset_pointer", "image_asset_pointer":
			marker = "asset_pointer"
		case "file_id", "file_ids", "fileid", "fileids":
			marker = "file_id_key"
		case "sediment", "sediment_id", "sediment_ids":
			marker = "sediment_key"
		case "image_url", "image_urls":
			marker = "image_url_key"
		case "download_url", "download_urls":
			marker = "download_url_key"
		case "generated_image", "generated_images", "images":
			marker = "generated_images_key"
		case "attachments":
			marker = "attachments_key"
		}
		if marker == "" {
			return
		}
		candidateKeys[lower] = true
		if value != nil && strings.TrimSpace(fmt.Sprint(value)) != "" {
			candidateValueCount++
		}
		if marker == "asset_pointer" || marker == "file_id_key" || marker == "sediment_key" {
			referenceMarkers[marker] = true
		} else {
			referenceMarkers[marker] = true
		}
	}
	observeText := func(text string) {
		lowerText := strings.ToLower(text)
		switch {
		case strings.Contains(lowerText, "file-service://"):
			referenceMarkers["file_service_url"] = true
		case strings.Contains(lowerText, "sediment://"):
			referenceMarkers["sediment_url"] = true
		case realFileIDRE.MatchString(text):
			referenceMarkers["file_id_value"] = true
		}
	}

	signals := AnalyzeImageConversation(value)
	if signals.ToolSeen {
		toolMarkers["image_tool"] = true
	}
	if signals.AssistantTextSeen {
		toolMarkers["assistant_text"] = true
	}
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case map[string]any:
			addRoleFromMap(typed, addRole)
			for key, child := range typed {
				lowerKey := strings.ToLower(strings.TrimSpace(key))
				switch lowerKey {
				case "status", "state", "finish_status", "finish_reason", "reason":
					if text, ok := child.(string); ok {
						addStatus(text)
					}
				case "is_error":
					if imageToolTruthy(child) {
						statusMarkers["is_error_true"] = true
					}
				}
				addReferenceKey(key, child)
				if text, ok := child.(string); ok {
					observeText(text)
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case string:
			observeText(typed)
		}
	}
	walk(value)
	summary.Roles = sortedDiagnosticValues(roles)
	summary.ToolMarkers = sortedDiagnosticValues(toolMarkers)
	summary.StatusMarkers = sortedDiagnosticValues(statusMarkers)
	summary.ReferenceMarkers = sortedDiagnosticValues(referenceMarkers)
	summary.CandidateKeys = sortedDiagnosticValues(candidateKeys)
	summary.CandidateValueCount = candidateValueCount
	return summary
}

func addRoleFromMap(value map[string]any, add func(string)) {
	if role, ok := value["role"].(string); ok {
		add(role)
	}
	if author, ok := value["author"].(map[string]any); ok {
		if role, ok := author["role"].(string); ok {
			add(role)
		}
	}
}

func sortedDiagnosticValues(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
