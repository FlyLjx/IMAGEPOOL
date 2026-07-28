package images

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const SuperResolution2KSuffix = "-2k"

// PrepareModelRequest converts a public local-processing alias into the
// original upstream model while retaining the requested output behavior.
func PrepareModelRequest(req Request) (Request, string) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = PublicImageModel
	}
	publicModel := PublicModelName(model)
	req.PublicModel = publicModel
	if base, ok := SuperResolutionBaseModel(model); ok {
		req.Model = base
		req.Size = SuperResolutionTargetSize(model, req.Size)
		req.SuperResolution = true
		return req, publicModel
	}
	req.Model = model
	if req.SuperResolution && strings.TrimSpace(req.Size) == "" {
		req.Size = "2048x2048"
	}
	return req, publicModel
}

func PublicModelName(model string) string {
	model = strings.TrimSpace(model)
	if _, ok := SuperResolutionBaseModel(model); ok {
		return model
	}
	return PublicImageModel
}

func SuperResolutionBaseModel(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if !strings.HasSuffix(model, SuperResolution2KSuffix) {
		return "", false
	}
	base := strings.TrimSuffix(model, SuperResolution2KSuffix)
	if !isGPTImageModel(base) {
		return "", false
	}
	return base, true
}

func SuperResolutionTargetSize(model, requestedSize string) string {
	if _, ok := SuperResolutionBaseModel(model); !ok {
		return strings.TrimSpace(requestedSize)
	}
	width, height, ok := parseImageSize(requestedSize)
	if !ok || width == height {
		return "2048x2048"
	}
	if width > height {
		return fmt.Sprintf("2048x%d", scaledDimension(height, width))
	}
	return fmt.Sprintf("%dx2048", scaledDimension(width, height))
}

func ExpandSuperResolutionModels(models []string) []string {
	seen := make(map[string]bool, len(models)*2)
	result := make([]string, 0, len(models)*2)
	for _, raw := range models {
		model := strings.TrimSpace(raw)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		result = append(result, model)
		variant := ""
		if isGPTImageModel(model) {
			variant = model + SuperResolution2KSuffix
		}
		if variant != "" && !seen[variant] {
			seen[variant] = true
			result = append(result, variant)
		}
	}
	return result
}

func isGPTImageModel(model string) bool {
	return strings.TrimSpace(model) == PublicImageModel
}

func parseImageSize(value string) (int, int, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	return width, height, widthErr == nil && heightErr == nil && width > 0 && height > 0
}

func scaledDimension(shortEdge, longEdge int) int {
	value := float64(shortEdge) * 2048 / float64(longEdge)
	return max(8, int(math.Round(value/8))*8)
}
