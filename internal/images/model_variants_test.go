package images

import (
	"reflect"
	"testing"
)

func TestExpandPublicModelsDeduplicatesAndKeepsBaseModel(t *testing.T) {
	models := ExpandPublicModels([]string{
		"gpt-image-2",
		"gpt-image-2",
		"codex-gpt-image-2",
	})
	want := []string{
		"gpt-image-2",
		"codex-gpt-image-2",
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models=%#v want=%#v", models, want)
	}
}

func TestPrepareModelRequestUsesBasePublicModel(t *testing.T) {
	req, publicModel := PrepareModelRequest(Request{Model: "gpt-image-2", Size: "1024x1536"})
	if req.Model != "gpt-image-2" || publicModel != "gpt-image-2" || req.PublicModel != "gpt-image-2" {
		t.Fatalf("request=%#v public=%q", req, publicModel)
	}
	if req.Size != "1024x1536" {
		t.Fatalf("size=%q", req.Size)
	}
}
