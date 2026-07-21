package openaiweb

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	fileServiceRE    = regexp.MustCompile(`file-service://([A-Za-z0-9_-]+)`)
	realFileIDRE     = regexp.MustCompile(`\bfile_00000000[a-f0-9]{24}\b`)
	sedimentRE       = regexp.MustCompile(`sediment://([A-Za-z0-9_-]+)`)
	conversationIDRE = regexp.MustCompile(`"conversation_id"\s*:\s*"([^"]+)"`)
)

func ExtractConversationID(payload string) string {
	if m := conversationIDRE.FindStringSubmatch(payload); len(m) == 2 {
		return m[1]
	}
	var v any
	if json.Unmarshal([]byte(payload), &v) == nil {
		return findStringKey(v, "conversation_id")
	}
	return ""
}

func ExtractImageReferenceIDs(v any) (fileIDs []string, sedimentIDs []string) {
	return extractImageReferenceIDs(v, false)
}

func ExtractGeneratedImageReferenceIDs(v any) (fileIDs []string, sedimentIDs []string) {
	return extractImageReferenceIDs(v, true)
}

func extractImageReferenceIDs(v any, generatedOnly bool) (fileIDs []string, sedimentIDs []string) {
	add := func(dst *[]string, values []string) {
		seen := map[string]bool{}
		for _, existing := range *dst {
			seen[existing] = true
		}
		for _, value := range values {
			if value == "" || seen[value] {
				continue
			}
			*dst = append(*dst, value)
			seen[value] = true
		}
	}
	var walk func(any, bool)
	walk = func(x any, allowExtract bool) {
		switch value := x.(type) {
		case string:
			if generatedOnly && !allowExtract {
				return
			}
			add(&fileIDs, submatchValues(fileServiceRE, value, 1))
			add(&fileIDs, realFileIDRE.FindAllString(value, -1))
			add(&sedimentIDs, submatchValues(sedimentRE, value, 1))
		case map[string]any:
			if generatedOnly {
				if role, ok := nodeRole(value); ok {
					role = strings.ToLower(role)
					if role == "user" {
						return
					}
					allowExtract = allowExtract || role == "assistant" || role == "tool"
				}
			}
			for _, child := range value {
				walk(child, allowExtract)
			}
		case []any:
			for _, child := range value {
				walk(child, allowExtract)
			}
		}
	}
	walk(v, !generatedOnly)
	return fileIDs, sedimentIDs
}

func nodeRole(v map[string]any) (string, bool) {
	if role, ok := v["role"].(string); ok && role != "" {
		return role, true
	}
	if author, ok := v["author"].(map[string]any); ok {
		if role, ok := author["role"].(string); ok && role != "" {
			return role, true
		}
	}
	if message, ok := v["message"].(map[string]any); ok {
		return nodeRole(message)
	}
	return "", false
}

func submatchValues(re *regexp.Regexp, s string, group int) []string {
	matches := re.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > group {
			out = append(out, m[group])
		}
	}
	return out
}

func findStringKey(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x[key].(string); ok && s != "" {
			return s
		}
		for _, child := range x {
			if got := findStringKey(child, key); got != "" {
				return got
			}
		}
	case []any:
		for _, child := range x {
			if got := findStringKey(child, key); got != "" {
				return got
			}
		}
	}
	return ""
}

// findImageGenerationTerminalError identifies terminal states emitted by the
// image tool. Once ChatGPT has ended a tool run, polling cannot produce an
// image, so callers should immediately switch accounts instead of consuming
// the entire polling window.
func findImageGenerationTerminalError(v any) error {
	status := findImageGenerationTerminalStatus(v)
	if status == "" {
		return nil
	}
	return fmt.Errorf("%w: ChatGPT 生图任务已终止（%s）", ErrImageGenerationTerminated, status)
}

func findImageGenerationTerminalStatus(v any) string {
	terminal := map[string]bool{
		"server_timeout":          true,
		"interrupted":             true,
		"failed":                  true,
		"failure":                 true,
		"error":                   true,
		"server_error":            true,
		"generation_failed":       true,
		"image_generation_failed": true,
		"cancelled":               true,
		"canceled":                true,
		"aborted":                 true,
	}
	var walk func(any) string
	walk = func(value any) string {
		switch item := value.(type) {
		case map[string]any:
			if status := imageToolTerminalStatus(item, terminal); status != "" {
				return status
			}
			for _, child := range item {
				if status := walk(child); status != "" {
					return status
				}
			}
		case []any:
			for _, child := range item {
				if status := walk(child); status != "" {
					return status
				}
			}
		}
		return ""
	}
	return walk(v)
}

// imageToolTerminalStatus only inspects messages produced by ChatGPT's image
// tool. Conversation events include many unrelated status and error fields;
// treating those as image failures would prematurely abandon healthy work.
func imageToolTerminalStatus(value map[string]any, terminal map[string]bool) string {
	message := value
	if nested, ok := value["message"].(map[string]any); ok {
		message = nested
	}
	role, ok := nodeRole(message)
	if !ok || !strings.EqualFold(strings.TrimSpace(role), "tool") {
		return ""
	}
	metadata, _ := message["metadata"].(map[string]any)
	if !isImageGenerationTool(message, metadata) {
		return ""
	}
	// Current ImageGen tool failures may omit metadata.status entirely and
	// instead mark the tool message with is_error=true. Treat that as a
	// terminal generation result so polling can switch accounts immediately.
	if imageToolTruthy(metadata["is_error"]) {
		return "image_generation_failed"
	}
	if status := terminalImageStatus(metadata["status"], terminal); status != "" {
		return status
	}
	if finish, ok := metadata["finish_details"].(map[string]any); ok {
		for _, key := range []string{"status", "type", "reason"} {
			if status := terminalImageStatus(finish[key], terminal); status != "" {
				return status
			}
		}
	}
	if imageToolErrorText(message) {
		return "image_generation_failed"
	}
	return ""
}

func imageToolErrorText(message map[string]any) bool {
	content, _ := message["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	for _, part := range parts {
		text, ok := part.(string)
		if !ok {
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(text))
		if strings.Contains(lower, "error when generating images") ||
			strings.Contains(lower, "unable to generate images") ||
			strings.Contains(lower, "failed to generate images") ||
			strings.Contains(text, "生成图片时遇到错误") ||
			strings.Contains(text, "图片生成失败") {
			return true
		}
	}
	return false
}

func isImageGenerationTool(message, metadata map[string]any) bool {
	if imageToolTruthy(metadata["image_gen_async"]) {
		return true
	}
	for _, key := range []string{"tool_name", "recipient", "model_slug", "invoked_plugin"} {
		if looksLikeImageTool(str(metadata[key])) {
			return true
		}
	}
	author, _ := message["author"].(map[string]any)
	for _, key := range []string{"name", "recipient"} {
		if looksLikeImageTool(str(author[key])) {
			return true
		}
	}
	return false
}

func looksLikeImageTool(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(value, "image_gen") || strings.Contains(value, "imagegen") || strings.Contains(value, "t2uay3k")
}

func imageToolTruthy(value any) bool {
	switch item := value.(type) {
	case bool:
		return item
	case string:
		return strings.EqualFold(strings.TrimSpace(item), "true") || strings.TrimSpace(item) == "1"
	case float64:
		return item != 0
	case int:
		return item != 0
	default:
		return false
	}
}

func terminalImageStatus(value any, terminal map[string]bool) string {
	status := normalizeImageGenerationStatus(str(value))
	if terminal[status] {
		return status
	}
	return ""
}

func normalizeImageGenerationStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}
