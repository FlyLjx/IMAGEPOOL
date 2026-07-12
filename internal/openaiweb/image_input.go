package openaiweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

var dataURLRE = regexp.MustCompile(`^data:([^;]+);base64,(.*)$`)

func ImageInputFromBytes(name, mimeType string, data []byte) (ImageInput, error) {
	if len(data) == 0 {
		return ImageInput{}, fmt.Errorf("empty image")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ImageInput{}, fmt.Errorf("cannot identify image file: %w", err)
	}
	if mimeType == "" {
		mimeType = "image/" + format
		if format == "jpg" {
			mimeType = "image/jpeg"
		}
	}
	if name == "" {
		ext := ".png"
		if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
			ext = exts[0]
		}
		name = "image" + ext
	}
	return ImageInput{Data: data, FileName: filepath.Base(name), MIMEType: mimeType, Width: cfg.Width, Height: cfg.Height}, nil
}

func ImageInputFromSource(ctx context.Context, client *http.Client, source string) (ImageInput, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return ImageInput{}, fmt.Errorf("empty image source")
	}
	if m := dataURLRE.FindStringSubmatch(source); len(m) == 3 {
		data, err := base64.StdEncoding.DecodeString(m[2])
		if err != nil {
			return ImageInput{}, err
		}
		return ImageInputFromBytes("image", m[1], data)
	}
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		if client == nil {
			client = http.DefaultClient
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return ImageInput{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return ImageInput{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return ImageInput{}, fmt.Errorf("download image %s status=%d", source, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 30<<20))
		if err != nil {
			return ImageInput{}, err
		}
		return ImageInputFromBytes(filepath.Base(resp.Request.URL.Path), resp.Header.Get("Content-Type"), data)
	}
	data, err := base64.StdEncoding.DecodeString(source)
	if err != nil {
		return ImageInput{}, err
	}
	return ImageInputFromBytes("image", "", data)
}
