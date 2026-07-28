package images

import (
	"reflect"
	"testing"
)

func TestExpandSuperResolutionModelsAddsFreeLocal2KVariants(t *testing.T) {
	models := ExpandSuperResolutionModels([]string{
		"gpt-image-2",
		"codex-gpt-image-2",
		"plus-codex-gpt-image-2",
	})
	want := []string{
		"gpt-image-2",
		"gpt-image-2-2k",
		"codex-gpt-image-2",
		"plus-codex-gpt-image-2",
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models=%#v want=%#v", models, want)
	}
}

func TestPrepareModelRequestConverts2KVariantToBaseModel(t *testing.T) {
	tests := []struct {
		name string
		size string
		want string
	}{
		{name: "default square", want: "2048x2048"},
		{name: "square", size: "1024x1024", want: "2048x2048"},
		{name: "landscape", size: "1536x1024", want: "2048x1368"},
		{name: "portrait", size: "1024x1536", want: "1368x2048"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, publicModel := PrepareModelRequest(Request{Model: "gpt-image-2-2k", Size: tt.size})
			if req.Model != "gpt-image-2" || publicModel != "gpt-image-2-2k" {
				t.Fatalf("model=%q public=%q", req.Model, publicModel)
			}
			if !req.SuperResolution || req.Size != tt.want {
				t.Fatalf("super_resolution=%t size=%q want=%q", req.SuperResolution, req.Size, tt.want)
			}
		})
	}
}
