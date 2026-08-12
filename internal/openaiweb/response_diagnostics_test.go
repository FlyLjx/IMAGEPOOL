package openaiweb

import (
	"strings"
	"testing"
)

func TestSummarizeImageResponseCapturesShapeWithoutRawValues(t *testing.T) {
	value := map[string]any{
		"mapping": map[string]any{
			"tool": map[string]any{
				"message": map[string]any{
					"author":   map[string]any{"role": "tool"},
					"metadata": map[string]any{"image_gen_async": true, "status": "in_progress"},
					"content":  map[string]any{"parts": []any{"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"}},
				},
			},
		},
		"image_url": "https://example.invalid/private-image-url",
	}

	summary := summarizeImageResponse(value, 321, nil)
	if summary.Bytes != 321 || len(summary.Fingerprint) != 16 {
		t.Fatalf("summary size/fingerprint=%#v", summary)
	}
	if !containsDiagnosticValue(summary.Roles, "tool") || !containsDiagnosticValue(summary.ToolMarkers, "image_tool") {
		t.Fatalf("summary roles/tools=%#v/%#v", summary.Roles, summary.ToolMarkers)
	}
	if !containsDiagnosticValue(summary.ReferenceMarkers, "file_service_url") || !containsDiagnosticValue(summary.CandidateKeys, "image_url") {
		t.Fatalf("summary reference markers/keys=%#v/%#v", summary.ReferenceMarkers, summary.CandidateKeys)
	}
	if summary.RawFileReferenceCount != 1 || summary.FileReferenceCount != 1 {
		t.Fatalf("summary file refs=%#v", summary)
	}

	var diagnostics ImageResponseDiagnostics
	diagnostics.Observe(summary)
	fields := diagnostics.LogFields()
	for _, leaked := range []string{"file_00000000aaaaaaaaaaaaaaaaaaaaaaaa", "https://example.invalid/private-image-url"} {
		if strings.Contains(fields, leaked) {
			t.Fatalf("diagnostic fields leaked %q: %s", leaked, fields)
		}
	}
	if !strings.Contains(fields, "response_tools=image_tool") || !strings.Contains(fields, "response_statuses=in_progress") || !strings.Contains(fields, "response_candidate_keys=image_url") {
		t.Fatalf("diagnostic fields=%s", fields)
	}
}

func TestSummarizeImageResponseSeparatesExcludedReferences(t *testing.T) {
	value := map[string]any{
		"message": map[string]any{
			"author":  map[string]any{"role": "user"},
			"content": map[string]any{"parts": []any{"file-service://file_uploaded"}},
		},
	}
	summary := summarizeImageResponse(value, 0, map[string]bool{"file_uploaded": true})
	if summary.RawFileReferenceCount != 1 || summary.FileReferenceCount != 0 {
		t.Fatalf("summary=%#v", summary)
	}
}

func TestSummarizeImageResponseCapturesBoundedAssistantTextForTerminalDiagnostics(t *testing.T) {
	value := map[string]any{
		"message": map[string]any{
			"author":  map[string]any{"role": "assistant"},
			"content": map[string]any{"parts": []any{"上游返回的诊断文本\n请稍后重试"}},
		},
	}
	summary := summarizeImageResponse(value, 0, nil)
	if summary.AssistantTextSample != "上游返回的诊断文本 请稍后重试" || summary.AssistantTextChars != 15 {
		t.Fatalf("summary assistant text=%#v", summary)
	}

	var diagnostics ImageResponseDiagnostics
	diagnostics.Observe(summary)
	if strings.Contains(diagnostics.LogFields(), "assistant_text_sample") {
		t.Fatalf("heartbeat diagnostics unexpectedly included text: %s", diagnostics.LogFields())
	}
	fields := diagnostics.LogFieldsWithAssistantText()
	if !strings.Contains(fields, "assistant_text_chars=15") || !strings.Contains(fields, "assistant_text_sample=\"上游返回的诊断文本 请稍后重试\"") {
		t.Fatalf("terminal diagnostics=%s", fields)
	}
}

func TestAssistantTextDetailsPrefersChineseOverEnglish(t *testing.T) {
	value := map[string]any{
		"mapping": map[string]any{
			"english": map[string]any{"message": map[string]any{
				"author":  map[string]any{"role": "assistant"},
				"content": map[string]any{"parts": []any{"Please update your prompt and try again."}},
			}},
			"chinese": map[string]any{"message": map[string]any{
				"author":  map[string]any{"role": "assistant"},
				"content": map[string]any{"parts": []any{"请修改提示词后重新提交。"}},
			}},
		},
	}
	text, chars := assistantTextDetails(value)
	if text != "请修改提示词后重新提交。" || chars != 12 {
		t.Fatalf("text=%q chars=%d", text, chars)
	}
}

func containsDiagnosticValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
