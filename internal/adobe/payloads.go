package adobe

import (
	"fmt"
	"strings"
	"time"
)

func imagePayload(model Model, prompt, quality string, sourceImageIDs []string) (map[string]any, error) {
	seed := time.Now().UnixNano() % 999999
	if model.UpstreamModelID == "gpt-image" {
		size, err := gptImageSize(model.AspectRatio, model.OutputResolution)
		if err != nil {
			return nil, err
		}
		detail := 1
		switch strings.ToLower(strings.TrimSpace(quality)) {
		case "high", "hd", "4k", "ultra":
			detail = 5
		case "medium", "2k", "auto", "":
			detail = 3
		}
		references := make([]map[string]any, 0, len(sourceImageIDs))
		for _, id := range sourceImageIDs {
			references = append(references, map[string]any{"id": id, "usage": "subject"})
		}
		modelSpecificPayload := map[string]any{
			"size": fmt.Sprintf("%dx%d", size["width"], size["height"]),
		}
		if len(references) > 0 {
			// adobe2api uses subject references without the text-only size field.
			modelSpecificPayload = map[string]any{}
		}
		return map[string]any{
			"modelId": model.UpstreamModelID, "modelVersion": model.UpstreamModelVersion, "n": 1, "prompt": prompt,
			"seeds": []int64{seed}, "output": map[string]any{"storeInputs": true}, "referenceBlobs": references,
			"generationMetadata":   map[string]any{"module": "text2image", "submodule": "ff-image-generate"},
			"modelSpecificPayload": modelSpecificPayload, "generationSettings": map[string]any{"detailLevel": detail},
			"size": size, "outputResolution": model.OutputResolution,
		}, nil
	}
	size, err := nanoImageSize(model.AspectRatio, model.OutputResolution)
	if err != nil {
		return nil, err
	}
	references := make([]map[string]any, 0, len(sourceImageIDs))
	for _, id := range sourceImageIDs {
		references = append(references, map[string]any{"id": id, "usage": "general"})
	}
	module := "text2image"
	if len(references) > 0 {
		module = "image2image"
	}
	return map[string]any{
		"modelId": model.UpstreamModelID, "modelVersion": model.UpstreamModelVersion, "n": 1, "prompt": prompt, "size": size,
		"seeds": []int64{seed}, "groundSearch": false, "skipCai": false, "output": map[string]any{"storeInputs": true},
		"referenceBlobs": references, "generationMetadata": map[string]any{"module": module, "submodule": "ff-image-generate"},
		"modelSpecificPayload": map[string]any{"aspectRatio": model.AspectRatio, "parameters": map[string]any{"addWatermark": false}},
	}, nil
}

func gptImageSize(ratio, resolution string) (map[string]int, error) {
	sizes := map[string]map[string][2]int{
		"1K": {
			"1:1": {1024, 1024}, "5:4": {1120, 896}, "9:16": {720, 1280}, "21:9": {1456, 624}, "16:9": {1280, 720},
			"4:3": {1152, 864}, "3:2": {1248, 832}, "4:5": {896, 1120}, "3:4": {864, 1152}, "2:3": {832, 1248},
		},
		"2K": {
			"1:1": {2048, 2048}, "5:4": {2240, 1792}, "9:16": {1440, 2560}, "21:9": {3024, 1296}, "16:9": {2560, 1440},
			"4:3": {2304, 1728}, "3:2": {2496, 1664}, "4:5": {1792, 2240}, "3:4": {1728, 2304}, "2:3": {1664, 2496},
		},
		"4K": {
			"1:1": {2880, 2880}, "5:4": {3200, 2560}, "9:16": {2160, 3840}, "21:9": {3696, 1584}, "16:9": {3840, 2160},
			"4:3": {3264, 2448}, "3:2": {3504, 2336}, "4:5": {2560, 3200}, "3:4": {2448, 3264}, "2:3": {2336, 3504},
		},
	}
	resolutionSizes, ok := sizes[strings.ToUpper(strings.TrimSpace(resolution))]
	if !ok {
		return nil, fmt.Errorf("unsupported GPT Image resolution %q", resolution)
	}
	dimensions, ok := resolutionSizes[ratio]
	if !ok {
		return nil, fmt.Errorf("unsupported GPT Image ratio %q", ratio)
	}
	return map[string]int{"width": dimensions[0], "height": dimensions[1]}, nil
}

func nanoImageSize(ratio, resolution string) (map[string]int, error) {
	sizes := map[string]map[string][2]int{
		"1K": {
			"1:1": {1024, 1024}, "1:8": {384, 3072}, "1:4": {512, 2048}, "16:9": {1360, 768}, "9:16": {768, 1360},
			"4:1": {2048, 512}, "4:3": {1152, 864}, "3:4": {864, 1152}, "8:1": {3072, 384},
		},
		"2K": {
			"1:1": {2048, 2048}, "1:8": {768, 6144}, "1:4": {1024, 4096}, "16:9": {2752, 1536}, "9:16": {1536, 2752},
			"4:1": {4096, 1024}, "4:3": {2048, 1536}, "3:4": {1536, 2048}, "8:1": {6144, 768},
		},
		"4K": {
			"1:1": {4096, 4096}, "1:8": {1536, 12288}, "1:4": {2048, 8192}, "16:9": {5504, 3072}, "9:16": {3072, 5504},
			"4:1": {8192, 2048}, "4:3": {4096, 3072}, "3:4": {3072, 4096}, "8:1": {12288, 1536},
		},
	}
	resolutionSizes, ok := sizes[strings.ToUpper(resolution)]
	if !ok {
		return nil, fmt.Errorf("unsupported Nano Banana resolution %q", resolution)
	}
	dimensions, ok := resolutionSizes[ratio]
	if !ok {
		return nil, fmt.Errorf("unsupported Nano Banana ratio %q", ratio)
	}
	return map[string]int{"width": dimensions[0], "height": dimensions[1]}, nil
}
