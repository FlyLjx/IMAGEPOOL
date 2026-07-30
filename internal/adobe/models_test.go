package adobe

import (
	"strings"
	"testing"
)

func TestModelCatalog(t *testing.T) {
	models := Models()
	if len(models) != 72 {
		t.Fatalf("model count=%d want=72", len(models))
	}
	for _, model := range models {
		if strings.HasPrefix(model.ID, legacyNanoBananaProPrefix) {
			t.Fatalf("legacy alias was exposed in public catalog: %s", model.ID)
		}
	}
	if _, err := ResolveModel("firefly-nano-banana-pro-2k-16x9"); err != nil {
		t.Fatalf("legacy alias must remain callable: %v", err)
	}
	tests := map[string]Model{
		"firefly-nano-banana-pro-2k-16x9": {UpstreamModelID: "gemini-flash", UpstreamModelVersion: "nano-banana-2", OutputResolution: "2K", AspectRatio: "16:9"},
		"firefly-nano-banana2-4k-1x8":     {UpstreamModelID: "gemini-flash", UpstreamModelVersion: "nano-banana-3", OutputResolution: "4K", AspectRatio: "1:8"},
		"firefly-gpt-image-1k-21x9":       {UpstreamModelID: "gpt-image", UpstreamModelVersion: "2", OutputResolution: "1K", AspectRatio: "21:9"},
	}
	for id, want := range tests {
		got, err := ResolveModel(id)
		if err != nil {
			t.Fatalf("resolve %s: %v", id, err)
		}
		if got.UpstreamModelID != want.UpstreamModelID || got.UpstreamModelVersion != want.UpstreamModelVersion || got.OutputResolution != want.OutputResolution || got.AspectRatio != want.AspectRatio {
			t.Fatalf("model %s=%#v want fields %#v", id, got, want)
		}
	}
	if IsModel("gpt-image-2") {
		t.Fatal("legacy OpenAI model was classified as Adobe")
	}
	if !IsRequestedModel("firefly-unknown-model") || IsRequestedModel("gpt-image-2") {
		t.Fatal("Adobe provider prefix classification is incorrect")
	}
}
