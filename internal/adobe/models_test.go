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

func TestResolveRequestedModelKeepsExactVariantAuthoritative(t *testing.T) {
	resolved, err := ResolveRequestedModel("firefly-gpt-image-2k-16x9", "1:1")
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
