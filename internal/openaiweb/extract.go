package openaiweb

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ImageConversationSignals is the compact signature of one conversation
// response. It lets the scheduler distinguish a real image-tool execution
// from a successful HTTP response that only contains ordinary chat messages.
type ImageConversationSignals struct {
	ToolSeen           bool
	ImageReferenceSeen bool
	AssistantTextSeen  bool
	LastRole           string
	Signature          string
}

func AnalyzeImageConversation(v any) ImageConversationSignals {
	signals := ImageConversationSignals{}
	roles := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			if role, ok := nodeRole(item); ok {
				role = strings.ToLower(strings.TrimSpace(role))
				if role != "" {
					roles[role] = true
					signals.LastRole = role
				}
				if role == "assistant" && hasAssistantText(item) {
					signals.AssistantTextSeen = true
				}
				if role == "tool" {
					metadata, _ := item["metadata"].(map[string]any)
					if isImageGenerationTool(item, metadata) {
						signals.ToolSeen = true
					}
				}
			}
			for _, child := range item {
				walk(child)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(v)
	fileIDs, sedimentIDs := ExtractGeneratedImageReferenceIDs(v)
	signals.ImageReferenceSeen = len(fileIDs) > 0 || len(sedimentIDs) > 0
	roleNames := make([]string, 0, len(roles))
	for role := range roles {
		roleNames = append(roleNames, role)
	}
	sort.Strings(roleNames)
	switch {
	case signals.ToolSeen && signals.ImageReferenceSeen:
		signals.Signature = "image_tool_with_reference"
	case signals.ToolSeen:
		signals.Signature = "image_tool_without_reference"
	case signals.AssistantTextSeen:
		signals.Signature = "assistant_text_only"
	case len(roleNames) > 0:
		signals.Signature = "roles_" + strings.Join(roleNames, "+")
	default:
		signals.Signature = "empty"
	}
	return signals
}

// MergeImageConversationSignals folds one conversation response into the
// attempt diagnostics. Polling receives many snapshots, so the diagnostic
// state is deliberately cumulative: a tool node or image reference observed
// on an earlier snapshot must remain visible on the final attempt record.
func MergeImageConversationSignals(diagnostics *ImageAttemptDiagnostics, value any) {
	if diagnostics == nil {
		return
	}
	signals := AnalyzeImageConversation(value)
	diagnostics.ToolSeen = diagnostics.ToolSeen || signals.ToolSeen
	diagnostics.ImageReferenceSeen = diagnostics.ImageReferenceSeen || signals.ImageReferenceSeen
	diagnostics.AssistantTextSeen = diagnostics.AssistantTextSeen || signals.AssistantTextSeen
	if signals.LastRole != "" {
		diagnostics.LastRole = signals.LastRole
	}
	if imageSignalRank(signals.Signature) >= imageSignalRank(diagnostics.ResultSignature) {
		diagnostics.ResultSignature = signals.Signature
	}
}

func imageSignalRank(signature string) int {
	switch signature {
	case "image_tool_with_reference":
		return 5
	case "image_tool_without_reference":
		return 4
	case "assistant_text_only":
		return 3
	case "roles_assistant+tool", "roles_tool+user", "roles_assistant+user+tool":
		return 2
	case "empty":
		return 0
	default:
		if strings.HasPrefix(signature, "roles_") {
			return 1
		}
		return 0
	}
}

func hasAssistantText(value map[string]any) bool {
	return strings.TrimSpace(assistantTextFromNode(value)) != ""
}

func assistantTextFromNode(value map[string]any) string {
	content, ok := value["content"].(map[string]any)
	if !ok {
		if nested, nestedOK := value["message"].(map[string]any); nestedOK {
			return assistantTextFromNode(nested)
		}
		return ""
	}
	if strings.TrimSpace(str(content["text"])) != "" {
		return strings.TrimSpace(str(content["text"]))
	}
	if parts, ok := content["parts"].([]any); ok {
		for _, part := range parts {
			if text := strings.TrimSpace(str(part)); text != "" {
				return text
			}
		}
	}
	return ""
}

// assistantTextDetails returns the first assistant text found in an upstream
// response. The full text stays in memory only; callers decide whether a
// bounded sample should be written to diagnostics.
func assistantTextDetails(value any) (string, int) {
	var text string
	var walk func(any)
	walk = func(item any) {
		if text != "" {
			return
		}
		switch typed := item.(type) {
		case map[string]any:
			if role, ok := nodeRole(typed); ok && strings.EqualFold(strings.TrimSpace(role), "assistant") {
				text = assistantTextFromNode(typed)
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	text = strings.Join(strings.Fields(text), " ")
	return text, len([]rune(text))
}

// DetectReferenceImageRequest inspects assistant-authored conversation text
// for a high-confidence request to upload a reference image or thumbnail.
// Prompt text and user messages are deliberately excluded: words such as
// "reference-style" or "for reference only" in a prompt are not enough to
// stop account dispatch.
func DetectReferenceImageRequest(v any) bool {
	for _, text := range assistantTextValues(v) {
		if isReferenceImageRequestText(text) {
			return true
		}
	}
	return false
}

func assistantTextValues(v any) []string {
	values := []string{}
	var walk func(any, bool)
	walk = func(value any, inheritedAssistant bool) {
		switch item := value.(type) {
		case map[string]any:
			isAssistant := inheritedAssistant
			if role, ok := nodeRole(item); ok {
				isAssistant = strings.EqualFold(strings.TrimSpace(role), "assistant")
			}
			if isAssistant {
				if text := assistantTextFromNode(item); strings.TrimSpace(text) != "" {
					values = append(values, text)
				}
			}
			for _, child := range item {
				walk(child, isAssistant)
			}
		case []any:
			for _, child := range item {
				walk(child, inheritedAssistant)
			}
		}
	}
	walk(v, false)
	return values
}

func isReferenceImageRequestText(text string) bool {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	if assistantChineseReferenceRequestRE.MatchString(text) || assistantChineseReferenceRequiredRE.MatchString(text) {
		return true
	}
	if assistantUploadThenImageRE.MatchString(lower) || assistantImageThenUploadRE.MatchString(lower) {
		return true
	}
	if assistantNeedSpecificReferenceRE.MatchString(lower) || assistantMustUploadReferenceRE.MatchString(lower) || assistantRequiredSpecificReferenceRE.MatchString(lower) {
		return true
	}
	return false
}

var (
	fileServiceRE                        = regexp.MustCompile(`file-service://([A-Za-z0-9_-]+)`)
	realFileIDRE                         = regexp.MustCompile(`\bfile_00000000[a-f0-9]{24}\b`)
	sedimentRE                           = regexp.MustCompile(`sediment://([A-Za-z0-9_-]+)`)
	conversationIDRE                     = regexp.MustCompile(`"conversation_id"\s*:\s*"([^"]+)"`)
	assistantUploadThenImageRE           = regexp.MustCompile(`(?is)\b(?:please\s+|kindly\s+)?(?:upload|attach|provide|send|share|submit|add|include)\b.{0,140}(?:\bimage\b|\bphoto\b|\bpicture\b|\bfile\b|\bthumbnail\b)`)
	assistantImageThenUploadRE           = regexp.MustCompile(`(?is)(?:\bimage\b|\bphoto\b|\bpicture\b|\bfile\b|\bthumbnail\b).{0,140}\b(?:upload|attach|provide|send|share|submit|add|include)\b`)
	assistantNeedSpecificReferenceRE     = regexp.MustCompile(`(?is)\b(?:i|we)\s+(?:need|require)\b.{0,120}(?:\breference\s+(?:image|photo|picture|file)\b|\b(?:youtube\s+)?thumbnail(?:\s+(?:image|photo|picture|file))?\b|\b(?:source|original|input)\s+(?:image|photo|picture|file)\b)`)
	assistantMustUploadReferenceRE       = regexp.MustCompile(`(?is)\b(?:must|have\s+to|need\s+to)\s+(?:upload|attach|provide|send|share|submit)\b.{0,120}(?:\bimage\b|\bphoto\b|\bpicture\b|\bfile\b|\bthumbnail\b)`)
	assistantRequiredSpecificReferenceRE = regexp.MustCompile(`(?is)(?:\breference\s+(?:image|photo|picture|file)\b|\b(?:youtube\s+)?thumbnail(?:\s+(?:image|photo|picture|file))?\b|\b(?:source|original|input)\s+(?:image|photo|picture|file)\b).{0,40}\b(?:required|needed|necessary)\b|\b(?:required|needed|necessary)\b.{0,40}(?:\breference\s+(?:image|photo|picture|file)\b|\b(?:youtube\s+)?thumbnail(?:\s+(?:image|photo|picture|file))?\b|\b(?:source|original|input)\s+(?:image|photo|picture|file)\b)`)
	assistantChineseReferenceRequestRE   = regexp.MustCompile(`(?:请(?:先)?|需要|必须|麻烦|请您).{0,100}(?:上传|提供|附上|发送|添加|补充).{0,100}(?:参考图|参考图片|参考照片|缩略图|缩略图片|原图|输入图|图片|照片)|(?:参考图|参考图片|参考照片|缩略图|缩略图片|原图|输入图|图片|照片).{0,100}(?:请)?(?:上传|提供|附上|发送|添加|补充)`)
	assistantChineseReferenceRequiredRE  = regexp.MustCompile(`(?:需要|必须有|必须提供|缺少).{0,60}(?:参考图|参考图片|参考照片|缩略图|缩略图片|原图|输入图)`)
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
