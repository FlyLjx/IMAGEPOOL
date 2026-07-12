package openaiweb

import (
	"encoding/json"
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
