package images

import (
	"strings"

	adobeprovider "imagepool/internal/adobe"
)

func PrepareModelRequest(req Request) (Request, string) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = PublicImageModel
	}
	publicModel := PublicModelName(model)
	req.Model = model
	if adobeprovider.IsRequestedModel(model) {
		if resolved, err := adobeprovider.ResolveRequestedModel(model, req.AspectRatio); err == nil {
			req.Model = resolved.ID
		}
	} else {
		req.Model = publicModel
	}
	return req, publicModel
}

func PublicModelName(model string) string {
	model = strings.TrimSpace(model)
	if adobeprovider.IsRequestedModel(model) {
		return adobeprovider.PublicModelID(model)
	}
	return PublicImageModel
}
