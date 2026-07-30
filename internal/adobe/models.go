package adobe

import (
	"fmt"
	"sort"
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
}

var modelCatalog = buildModelCatalog()

func IsModel(id string) bool {
	_, ok := modelCatalog[strings.TrimSpace(id)]
	return ok
}

func IsRequestedModel(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), "firefly-")
}

func ResolveModel(id string) (Model, error) {
	id = strings.TrimSpace(id)
	model, ok := modelCatalog[id]
	if !ok {
		return Model{}, fmt.Errorf("invalid Adobe image model %q", id)
	}
	return model, nil
}

func Models() []Model {
	models := make([]Model, 0, len(modelCatalog))
	for _, model := range modelCatalog {
		// Kept resolvable for existing clients, but this is only a legacy alias
		// of nano-banana-2 and must not appear as a separate public model.
		if strings.HasPrefix(model.ID, legacyNanoBananaProPrefix) {
			continue
		}
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func buildModelCatalog() map[string]Model {
	catalog := map[string]Model{}
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
	registerNano := func(prefix, version, label string, ratios []struct{ ratio, suffix string }) {
		for _, resolution := range []string{"1k", "2k", "4k"} {
			for _, ratio := range ratios {
				id := fmt.Sprintf("%s-%s-%s", prefix, resolution, ratio.suffix)
				catalog[id] = Model{ID: id, Description: fmt.Sprintf("%s (%s %s)", label, strings.ToUpper(resolution), ratio.ratio), UpstreamModelID: "gemini-flash", UpstreamModelVersion: version, OutputResolution: strings.ToUpper(resolution), AspectRatio: ratio.ratio}
			}
		}
	}
	registerNano("firefly-nano-banana-pro", "nano-banana-2", "Firefly Nano Banana Pro", standardRatios)
	registerNano("firefly-nano-banana", "nano-banana-2", "Firefly Nano Banana", standardRatios)
	registerNano("firefly-nano-banana2", "nano-banana-3", "Firefly Nano Banana 2", nano2Ratios)
	for _, resolution := range []string{"1k", "2k", "4k"} {
		for _, ratio := range gptRatios {
			id := fmt.Sprintf("firefly-gpt-image-%s-%s", resolution, ratio.suffix)
			catalog[id] = Model{ID: id, Description: fmt.Sprintf("Firefly GPT Image (%s %s)", strings.ToUpper(resolution), ratio.ratio), UpstreamModelID: "gpt-image", UpstreamModelVersion: "2", OutputResolution: strings.ToUpper(resolution), AspectRatio: ratio.ratio}
		}
	}
	return catalog
}
