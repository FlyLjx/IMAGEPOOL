package adobe

import "testing"

func TestGPTImagePayloadShape(t *testing.T) {
	model, _ := ResolveModel("firefly-gpt-image-2k-16x9")
	payload, err := imagePayload(model, "draw", "medium", nil)
	if err != nil {
		t.Fatal(err)
	}
	if payload["modelId"] != "gpt-image" || payload["modelVersion"] != "2" {
		t.Fatalf("payload=%#v", payload)
	}
	settings := payload["generationSettings"].(map[string]any)
	if settings["detailLevel"] != 3 {
		t.Fatalf("settings=%#v", settings)
	}
	if _, ok := payload["size"]; ok {
		size := payload["size"].(map[string]int)
		if size["width"] != 2560 || size["height"] != 1440 {
			t.Fatalf("size=%#v", size)
		}
	} else {
		t.Fatal("GPT Image payload is missing top-level size")
	}
	if payload["outputResolution"] != "2K" {
		t.Fatalf("outputResolution=%#v", payload["outputResolution"])
	}
	modelSpecific := payload["modelSpecificPayload"].(map[string]any)
	if modelSpecific["size"] != "2560x1440" {
		t.Fatalf("modelSpecificPayload=%#v", modelSpecific)
	}
}

func TestGPTImageSizeCatalog(t *testing.T) {
	tests := []struct {
		resolution string
		ratio      string
		width      int
		height     int
	}{
		{"1K", "21:9", 1456, 624},
		{"2K", "2:3", 1664, 2496},
		{"4K", "16:9", 3840, 2160},
	}
	for _, test := range tests {
		size, err := gptImageSize(test.ratio, test.resolution)
		if err != nil {
			t.Fatal(err)
		}
		if size["width"] != test.width || size["height"] != test.height {
			t.Fatalf("%s %s size=%#v", test.resolution, test.ratio, size)
		}
	}
}

func TestNanoPayloadShape(t *testing.T) {
	model, _ := ResolveModel("firefly-nano-banana2-4k-1x8")
	payload, err := imagePayload(model, "draw", "", []string{"image-1"})
	if err != nil {
		t.Fatal(err)
	}
	size := payload["size"].(map[string]int)
	if size["width"] != 1536 || size["height"] != 12288 {
		t.Fatalf("size=%#v", size)
	}
	metadata := payload["generationMetadata"].(map[string]any)
	if metadata["module"] != "image2image" {
		t.Fatalf("metadata=%#v", metadata)
	}
}
