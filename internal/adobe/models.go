package adobe

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const DefaultModelID = "firefly-nano-banana-2k-16x9"

const legacyNanoBananaProPrefix = "firefly-nano-banana-pro-"

type Model struct {
	ID                   string `json:"id"`
	Description          string `json:"description"`
	UpstreamModelID      string `json:"upstream_model_id"`
	UpstreamModelVersion string `json:"upstream_model_version"`
	OutputResolution     string `json:"output_resolution"`
	AspectRatio          string `json:"aspect_ratio"`
	PublicID             string `json:"-"`
}

var modelCatalog, publicModelCatalog, publicModelVariants = buildModelCatalog()

func IsModel(id string) bool {
	id = strings.TrimSpace(id)
	if _, ok := modelCatalog[id]; ok {
		return true
	}
	_, ok := publicModelCatalog[id]
	return ok
}

func IsRequestedModel(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), "firefly-")
}

func ResolveModel(id string) (Model, error) {
	id = strings.TrimSpace(id)
	if model, ok := modelCatalog[id]; ok {
		return model, nil
	}
	if model, ok := publicModelCatalog[id]; ok {
		return model, nil
	}
	return Model{}, fmt.Errorf("invalid Adobe image model %q", id)
}

// ResolveRequestedModel keeps exact legacy variant IDs authoritative. Public
// base IDs use aspectRatio to select a hidden variant of the same resolution.
func ResolveRequestedModel(id, aspectRatio string) (Model, error) {
	id = strings.TrimSpace(id)
	if model, ok := modelCatalog[id]; ok {
		return model, nil
	}
	if _, ok := publicModelCatalog[id]; !ok {
		return Model{}, fmt.Errorf("invalid Adobe image model %q", id)
	}
	variants := publicModelVariants[id]
	if model, ok := variants[aspectRatioKey(aspectRatio)]; ok {
		return model, nil
	}
	return variants[aspectRatioKey("1:1")], nil
}

func Models() []Model {
	models := make([]Model, 0, len(publicModelCatalog))
	for _, model := range publicModelCatalog {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func PublicModelID(id string) string {
	id = strings.TrimSpace(id)
	if model, ok := modelCatalog[id]; ok {
		return model.PublicID
	}
	if _, ok := publicModelCatalog[id]; ok {
		return id
	}
	return id
}

func buildModelCatalog() (map[string]Model, map[string]Model, map[string]map[string]Model) {
	catalog := map[string]Model{}
	public := map[string]Model{}
	variants := map[string]map[string]Model{}
	standardRatios := []struct{ ratio, suffix string }{
		{"1:1", "1x1"}, {"16:9", "16x9"}, {"9:16", "9x16"}, {"4:3", "4x3"}, {"3:4", "3x4"},
	}
	nano2Ratios := append(append([]struct{ ratio, suffix string }{}, standardRatios...),
		struct{ ratio, suffix string }{"1:8", "1x8"},
		struct{ ratio, suffix string }{"1:4", "1x4"},
		struct{ ratio, suffix string }{"4:1", "4x1"},
		struct{ ratio, suffix string }{"8:1", "8x1"},
	)
	gptRatios := []struct{ ratio, suffix string }{
		{"1:1", "1x1"}, {"5:4", "5x4"}, {"9:16", "9x16"}, {"21:9", "21x9"}, {"16:9", "16x9"},
		{"3:2", "3x2"}, {"4:3", "4x3"}, {"4:5", "4x5"}, {"3:4", "3x4"}, {"2:3", "2x3"},
	}
	registerNano := func(prefix, publicPrefix, version, label string, ratios []struct{ ratio, suffix string }) {
		for _, resolution := range []string{"1k", "2k", "4k"} {
			publicID := fmt.Sprintf("%s-%s", publicPrefix, resolution)
			if _, ok := public[publicID]; !ok {
				public[publicID] = Model{ID: publicID, PublicID: publicID, Description: fmt.Sprintf("%s (%s, aspect_ratio selectable)", label, strings.ToUpper(resolution)), UpstreamModelID: "gemini-flash", UpstreamModelVersion: version, OutputResolution: strings.ToUpper(resolution), AspectRatio: "1:1"}
			}
			if variants[publicID] == nil {
				variants[publicID] = map[string]Model{}
			}
			for _, ratio := range ratios {
				id := fmt.Sprintf("%s-%s-%s", prefix, resolution, ratio.suffix)
				model := Model{ID: id, PublicID: publicID, Description: fmt.Sprintf("%s (%s %s)", label, strings.ToUpper(resolution), ratio.ratio), UpstreamModelID: "gemini-flash", UpstreamModelVersion: version, OutputResolution: strings.ToUpper(resolution), AspectRatio: ratio.ratio}
				catalog[id] = model
				if prefix == publicPrefix {
					variants[publicID][aspectRatioKey(ratio.ratio)] = model
				}
			}
		}
	}
	registerNano("firefly-nano-banana-pro", "firefly-nano-banana", "nano-banana-2", "Firefly Nano Banana", standardRatios)
	registerNano("firefly-nano-banana", "firefly-nano-banana", "nano-banana-2", "Firefly Nano Banana", standardRatios)
	registerNano("firefly-nano-banana2", "firefly-nano-banana2", "nano-banana-3", "Firefly Nano Banana 2", nano2Ratios)
	for _, resolution := range []string{"1k", "2k", "4k"} {
		publicID := fmt.Sprintf("firefly-gpt-image-%s", resolution)
		public[publicID] = Model{ID: publicID, PublicID: publicID, Description: fmt.Sprintf("Firefly GPT Image (%s, aspect_ratio selectable)", strings.ToUpper(resolution)), UpstreamModelID: "gpt-image", UpstreamModelVersion: "2", OutputResolution: strings.ToUpper(resolution), AspectRatio: "1:1"}
		variants[publicID] = map[string]Model{}
		for _, ratio := range gptRatios {
			id := fmt.Sprintf("firefly-gpt-image-%s-%s", resolution, ratio.suffix)
			model := Model{ID: id, PublicID: publicID, Description: fmt.Sprintf("Firefly GPT Image (%s %s)", strings.ToUpper(resolution), ratio.ratio), UpstreamModelID: "gpt-image", UpstreamModelVersion: "2", OutputResolution: strings.ToUpper(resolution), AspectRatio: ratio.ratio}
			catalog[id] = model
			variants[publicID][aspectRatioKey(ratio.ratio)] = model
		}
	}
	return catalog, public, variants
}

func aspectRatioKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "x", ":")
	value = strings.ReplaceAll(value, "/", ":")
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return ""
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return ""
	}
	divisor := greatestCommonDivisor(width, height)
	return fmt.Sprintf("%d:%d", width/divisor, height/divisor)
}

func greatestCommonDivisor(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}
