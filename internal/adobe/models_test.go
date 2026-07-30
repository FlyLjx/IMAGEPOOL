package adobe

import (
	"reflect"
	"strings"
	"testing"
)

func TestModelCatalog(t *testing.T) {
	models := Models()
	if len(models) != 9 {
		t.Fatalf("model count=%d want=9", len(models))
	}
	gotIDs := make([]string, 0, len(models))
	for _, model := range models {
		gotIDs = append(gotIDs, model.ID)
	}
	wantIDs := []string{
		"firefly-gpt-image-1k", "firefly-gpt-image-2k", "firefly-gpt-image-4k",
		"firefly-nano-banana-1k", "firefly-nano-banana-2k", "firefly-nano-banana-4k",
		"firefly-nano-banana2-1k", "firefly-nano-banana2-2k", "firefly-nano-banana2-4k",
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("model IDs=%#v want=%#v", gotIDs, wantIDs)
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
	if !IsModel("firefly-gpt-image-2k") || IsModel("firefly-unknown-model") {
		t.Fatal("public Adobe model classification is incorrect")
	}
	public, err := ResolveModel("firefly-gpt-image-2k")
	if err != nil || !strings.Contains(public.Description, "aspect_ratio selectable") {
		t.Fatalf("public model=%#v err=%v", public, err)
	}
}

func TestResolveRequestedModelUsesAspectRatioWithinResolution(t *testing.T) {
	tests := []struct {
		model       string
		aspectRatio string
		want        string
	}{
		{"firefly-nano-banana-1k", "16:9", "firefly-nano-banana-1k-16x9"},
		{"firefly-nano-banana-2k", "9x16", "firefly-nano-banana-2k-9x16"},
		{"firefly-nano-banana-4k", "unsupported", "firefly-nano-banana-4k-1x1"},
		{"firefly-nano-banana2-1k", "1/8", "firefly-nano-banana2-1k-1x8"},
		{"firefly-nano-banana2-2k", "", "firefly-nano-banana2-2k-1x1"},
		{"firefly-nano-banana2-4k", "8:1", "firefly-nano-banana2-4k-8x1"},
		{"firefly-gpt-image-1k", "7:3", "firefly-gpt-image-1k-21x9"},
		{"firefly-gpt-image-2k", "3:2", "firefly-gpt-image-2k-3x2"},
		{"firefly-gpt-image-4k", "5:4", "firefly-gpt-image-4k-5x4"},
	}
	for _, test := range tests {
		resolved, err := ResolveRequestedModel(test.model, test.aspectRatio)
		if err != nil || resolved.ID != test.want {
			t.Fatalf("ResolveRequestedModel(%q, %q)=%q, %v; want %q", test.model, test.aspectRatio, resolved.ID, err, test.want)
		}
	}
}

func TestResolveRequestedModelUsesSizeAsAspectRatioFallback(t *testing.T) {
	tests := []struct {
		model       string
		aspectRatio string
		size        string
		want        string
	}{
		{"firefly-nano-banana-4k", "", "3072x1728", "firefly-nano-banana-4k-16x9"},
		{"firefly-nano-banana2-2k", "", "1152x2048", "firefly-nano-banana2-2k-9x16"},
		{"firefly-gpt-image-2k", "", "2048x1360", "firefly-gpt-image-2k-3x2"},
		{"firefly-gpt-image-4k", "", "1360x2048", "firefly-gpt-image-4k-2x3"},
		{"firefly-gpt-image-2k", "", "1792x1024", "firefly-gpt-image-2k-16x9"},
		{"firefly-nano-banana-2k", "", "1536x1024", "firefly-nano-banana-2k-4x3"},
		{"firefly-nano-banana2-2k", "", "1024x1536", "firefly-nano-banana2-2k-3x4"},
		{"firefly-gpt-image-2k", "4:3", "3072x1728", "firefly-gpt-image-2k-4x3"},
		{"firefly-gpt-image-2k", "2048x1360", "1024x1024", "firefly-gpt-image-2k-3x2"},
	}
	for _, test := range tests {
		resolved, err := ResolveRequestedModelWithSize(test.model, test.aspectRatio, test.size)
		if err != nil || resolved.ID != test.want {
			t.Fatalf("ResolveRequestedModelWithSize(%q, %q, %q)=%q, %v; want %q", test.model, test.aspectRatio, test.size, resolved.ID, err, test.want)
		}
	}
}

func TestResolveRequestedModelSupportsAIPAISizeMatrix(t *testing.T) {
	sizes := map[string]map[string]string{
		"1k": {
			"1:1": "1024x1024", "16:9": "1536x864", "9:16": "864x1536", "4:3": "1536x1152", "3:4": "1152x1536", "3:2": "1536x1024", "2:3": "1024x1536",
		},
		"2k": {
			"1:1": "2048x2048", "16:9": "2048x1152", "9:16": "1152x2048", "4:3": "2048x1536", "3:4": "1536x2048", "3:2": "2048x1360", "2:3": "1360x2048",
		},
		"4k": {
			"1:1": "3072x3072", "16:9": "3072x1728", "9:16": "1728x3072", "4:3": "3072x2304", "3:4": "2304x3072", "3:2": "3072x2048", "2:3": "2048x3072",
		},
	}
	for resolution, ratios := range sizes {
		for ratio, size := range ratios {
			resolved, err := ResolveRequestedModelWithSize("firefly-gpt-image-"+resolution, "", size)
			if err != nil || resolved.AspectRatio != ratio || resolved.OutputResolution != strings.ToUpper(resolution) {
				t.Fatalf("resolution=%s ratio=%s size=%s resolved=%#v err=%v", resolution, ratio, size, resolved, err)
			}
		}
	}
}

func TestResolveRequestedModelKeepsExactVariantAuthoritative(t *testing.T) {
	resolved, err := ResolveRequestedModelWithSize("firefly-gpt-image-2k-16x9", "1:1", "1024x1024")
	if err != nil || resolved.ID != "firefly-gpt-image-2k-16x9" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if public := PublicModelID(resolved.ID); public != "firefly-gpt-image-2k" {
		t.Fatalf("public model=%q", public)
	}
	if public := PublicModelID("firefly-nano-banana-pro-2k-16x9"); public != "firefly-nano-banana-2k" {
		t.Fatalf("legacy public model=%q", public)
	}
}
