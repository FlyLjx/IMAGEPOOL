package httpapi

import (
	"testing"

	"imagepool/internal/images"
	"imagepool/internal/openaiweb"
)

func TestImageRequestHashIncludesReferenceContents(t *testing.T) {
	base := images.Request{Prompt: "draw", Model: "firefly-image-4-ultra", References: []openaiweb.ImageInput{{Data: []byte("first")}}}
	changed := base
	changed.References = []openaiweb.ImageInput{{Data: []byte("second")}}
	if imageRequestHash(base) == imageRequestHash(changed) {
		t.Fatal("different reference images produced the same request hash")
	}
}

func TestImageRequestHashIsStable(t *testing.T) {
	request := images.Request{Prompt: "draw", Model: "firefly-image-4-ultra", N: 1, References: []openaiweb.ImageInput{{Data: []byte("reference")}}}
	if first, second := imageRequestHash(request), imageRequestHash(request); first == "" || first != second {
		t.Fatalf("first=%q second=%q", first, second)
	}
}

func TestAdobeImageRequestHashIgnoresSize(t *testing.T) {
	first := images.Request{Prompt: "draw", Model: "firefly-nano-banana-2k-16x9", Size: "1024x1024"}
	second := first
	second.Size = "4096x4096"
	if imageRequestHash(first) != imageRequestHash(second) {
		t.Fatal("Adobe request size changed the idempotency hash")
	}
}

func TestAdobeImageRequestHashIncludesAspectRatio(t *testing.T) {
	first := images.Request{Prompt: "draw", Model: "firefly-gpt-image-2k", AspectRatio: "1:1"}
	second := first
	second.AspectRatio = "16:9"
	if imageRequestHash(first) == imageRequestHash(second) {
		t.Fatal("Adobe request aspect_ratio did not change the idempotency hash")
	}
}

func TestAdobeImageRequestHashUsesEffectiveVariant(t *testing.T) {
	colon := images.Request{Prompt: "draw", Model: "firefly-gpt-image-2k", AspectRatio: "16:9"}
	xAlias := colon
	xAlias.AspectRatio = "16x9"
	if imageRequestHash(colon) != imageRequestHash(xAlias) {
		t.Fatal("equivalent Adobe aspect-ratio aliases changed the idempotency hash")
	}
	exact := images.Request{Prompt: "draw", Model: "firefly-gpt-image-2k-16x9", AspectRatio: "1:1"}
	exactChanged := exact
	exactChanged.AspectRatio = "4:3"
	if imageRequestHash(exact) != imageRequestHash(exactChanged) {
		t.Fatal("ignored aspect_ratio changed an exact Adobe variant hash")
	}
}
