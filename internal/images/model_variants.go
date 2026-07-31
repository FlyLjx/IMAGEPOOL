package images

import "strings"

// PrepareModelRequest normalizes public image requests to the upstream image model.
func PrepareModelRequest(req Request) (Request, string) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = PublicImageModel
	}
	req.Model = model
	req.PublicModel = PublicModelName(model)
	return req, req.PublicModel
}

func PublicModelName(model string) string {
	if strings.TrimSpace(model) == "" {
		return PublicImageModel
	}
	return PublicImageModel
}

func ExpandPublicModels(models []string) []string {
	seen := make(map[string]bool, len(models)+1)
	result := make([]string, 0, len(models)+1)
	for _, raw := range models {
		model := strings.TrimSpace(raw)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		result = append(result, model)
	}
	if !seen[PublicImageModel] {
		result = append(result, PublicImageModel)
	}
	return result
}
