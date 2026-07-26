package images

import (
	"encoding/json"
	"testing"

	"imagepool/internal/openaiweb"
)

func TestEstimateImageUsageForMediumLandscape(t *testing.T) {
	usage := estimateImageUsage(Request{
		Prompt:  "draw a production landscape",
		Size:    "1536x1024",
		Quality: "medium",
	}, 1)
	if usage == nil {
		t.Fatal("usage is nil")
	}
	if usage.InputTokens != 9607 || usage.OutputTokens != 6144 || usage.TotalTokens != 15751 {
		t.Fatalf("usage=%#v", usage)
	}
}

func TestEstimateImageUsageAccumulatesImagesAndReferences(t *testing.T) {
	usage := estimateImageUsage(Request{
		Prompt:  "改成绿色",
		Size:    "1024x1024",
		Quality: "high",
		References: []openaiweb.ImageInput{{
			Width: 512, Height: 256,
		}},
	}, 2)
	if usage == nil {
		t.Fatal("usage is nil")
	}
	if usage.InputTokens != 20232 || usage.OutputTokens != 32768 || usage.TotalTokens != 53000 {
		t.Fatalf("usage=%#v", usage)
	}
}

func TestWithEstimatedUsagePreservesExistingUsage(t *testing.T) {
	existing := &Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}
	response := withEstimatedUsage(Response{Data: []Data{{URL: "https://example.test/image.png"}}, Usage: existing}, Request{Prompt: "draw"})
	if response.Usage != existing {
		t.Fatalf("usage=%#v", response.Usage)
	}
}

func TestWithEstimatedUsageOmitsUsageWithoutImages(t *testing.T) {
	response := withEstimatedUsage(Response{}, Request{Prompt: "draw"})
	if response.Usage != nil {
		t.Fatalf("usage=%#v", response.Usage)
	}
}

func TestMarshalForOpenAIIncludesEstimatedUsage(t *testing.T) {
	response := withEstimatedUsage(Response{Data: []Data{{URL: "https://example.test/image.png"}}}, Request{
		Prompt: "draw",
		Size:   "1024x1024",
	})
	body, err := json.Marshal(response.MarshalForOpenAI())
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Usage.InputTokens != 9601 || payload.Usage.OutputTokens != 4096 || payload.Usage.TotalTokens != 13697 {
		t.Fatalf("body=%s", body)
	}
}
