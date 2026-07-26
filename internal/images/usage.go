package images

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const estimatedImageWebContextTokens = 9600

// Usage is the OpenAI-compatible token accounting returned with an image.
// ChatGPT Web does not expose billing usage, so IMAGEPOOL estimates it from
// the submitted context and generated pixel blocks when upstream usage is
// absent.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func withEstimatedUsage(response Response, req Request) Response {
	if response.Usage != nil && response.Usage.TotalTokens > 0 {
		return response
	}
	count := len(response.Data)
	if count == 0 {
		return response
	}
	response.Usage = estimateImageUsage(req, count)
	return response
}

func estimateImageUsage(req Request, imageCount int) *Usage {
	if imageCount <= 0 {
		return nil
	}
	width, height, ok := parseEstimatedImageSize(req.Size)
	if !ok {
		width, height = 1024, 1024
	}

	inputPerImage := estimatedImageWebContextTokens + estimateTextTokens(req.Prompt)
	for _, reference := range req.References {
		refWidth, refHeight := reference.Width, reference.Height
		if refWidth <= 0 || refHeight <= 0 {
			refWidth, refHeight = 1024, 1024
		}
		inputPerImage += imageBlockTokens(refWidth, refHeight)
	}

	outputPerImage := applyImageQuality(imageBlockTokens(width, height), req.Quality)
	usage := &Usage{
		InputTokens:  inputPerImage * imageCount,
		OutputTokens: outputPerImage * imageCount,
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return usage
}

func parseEstimatedImageSize(value string) (int, int, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	return width, height, widthErr == nil && heightErr == nil && width > 0 && height > 0
}

// Image generation is estimated as one token per 16x16 output pixel block.
func imageBlockTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	return int(math.Ceil(float64(width)/16)) * int(math.Ceil(float64(height)/16))
}

func applyImageQuality(tokens int, quality string) int {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "low":
		return max(1, int(math.Ceil(float64(tokens)/4)))
	case "high", "hd":
		return tokens * 4
	default:
		return max(1, tokens)
	}
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	asciiBytes, nonASCII := 0, 0
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		if r <= 0x7f {
			asciiBytes++
		} else {
			nonASCII++
		}
	}
	return int(math.Ceil(float64(asciiBytes)/4)) + nonASCII
}
