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
	req.Model = publicModel
	return req, publicModel
}

func PublicModelName(model string) string {
	model = strings.TrimSpace(model)
	if adobeprovider.IsRequestedModel(model) {
		return model
	}
	return PublicImageModel
}
