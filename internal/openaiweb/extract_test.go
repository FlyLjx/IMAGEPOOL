package openaiweb

import "testing"

func TestDetectReferenceImageRequestUsesAssistantTextOnly(t *testing.T) {
	tests := []struct {
		name string
		data any
		want bool
	}{
		{
			name: "english upload request",
			data: map[string]any{
				"message": map[string]any{
					"author":  map[string]any{"role": "assistant"},
					"content": map[string]any{"parts": []any{"Please upload a reference image before continuing."}},
				},
			},
			want: true,
		},
		{
			name: "chinese thumbnail request",
			data: map[string]any{
				"message": map[string]any{
					"author":  map[string]any{"role": "assistant"},
					"content": map[string]any{"parts": []any{"请先上传缩略图后继续。"}},
				},
			},
			want: true,
		},
		{
			name: "user prompt is ignored",
			data: map[string]any{
				"message": map[string]any{
					"author":  map[string]any{"role": "user"},
					"content": map[string]any{"parts": []any{"Please upload a reference image."}},
				},
			},
			want: false,
		},
		{
			name: "reference wording without request",
			data: map[string]any{
				"message": map[string]any{
					"author":  map[string]any{"role": "assistant"},
					"content": map[string]any{"parts": []any{"This style is for reference only; no image is needed."}},
				},
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DetectReferenceImageRequest(test.data); got != test.want {
				t.Fatalf("detected=%v want=%v", got, test.want)
			}
		})
	}
}
