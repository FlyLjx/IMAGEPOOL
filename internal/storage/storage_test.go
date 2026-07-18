package storage

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"imagepool/internal/config"
)

func TestCompressAndCleanupOperateOnlyOnImageRoot(t *testing.T) {
	cfg := config.Default()
	cfg.ImageOutputDir = t.TempDir()
	service := NewService(cfg)
	canvas := image.NewRGBA(image.Rect(0, 0, 128, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			canvas.SetRGBA(x, y, color.RGBA{R: 20, G: 80, B: 140, A: 255})
		}
	}
	buffer := new(bytes.Buffer)
	if err := png.Encode(buffer, canvas); err != nil {
		t.Fatal(err)
	}
	first, err := service.Save(buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Compress(); err != nil {
		t.Fatal(err)
	}
	f, _, err := service.Open(first.Rel)
	if err != nil {
		t.Fatal(err)
	}
	_, _, decodeErr := image.Decode(f)
	f.Close()
	if decodeErr != nil {
		t.Fatalf("compressed image is invalid: %v", decodeErr)
	}
	removed, _, paths, _, err := service.CleanupToFreeMB(1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(paths) != 1 || paths[0] != first.Rel {
		t.Fatalf("removed=%d paths=%#v", removed, paths)
	}
}

func TestListOpenDelete(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.ImageOutputDir = dir
	svc := NewService(cfg)
	img := filepath.Join(dir, "2026", "07", "11", "a.png")
	if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := svc.List("http://localhost", "", "")
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	f, _, err := svc.Open(items[0].Rel)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	removed, err := svc.Delete([]string{items[0].Rel})
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
}

func TestThumbnailServesLowResolutionCacheAndDeleteRemovesIt(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.ImageOutputDir = dir
	svc := NewService(cfg)
	canvas := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 1200; x++ {
			canvas.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 180, A: 255})
		}
	}
	buffer := new(bytes.Buffer)
	if err := png.Encode(buffer, canvas); err != nil {
		t.Fatal(err)
	}
	item, err := svc.Save(buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	thumb, name, _, err := svc.Thumbnail(item.Rel, 320)
	if err != nil {
		t.Fatal(err)
	}
	cfgThumb, format, err := image.DecodeConfig(thumb)
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || name == "" || cfgThumb.Width != 320 || cfgThumb.Height != 213 {
		t.Fatalf("thumbnail name=%q format=%s size=%dx%d", name, format, cfgThumb.Width, cfgThumb.Height)
	}
	if items, err := svc.List("http://localhost", "", ""); err != nil || len(items) != 1 {
		t.Fatalf("list must skip thumbnail cache: items=%#v err=%v", items, err)
	}
	cachePath := filepath.Join(dir, ".thumbnails", filepath.FromSlash(item.Rel)+".jpg")
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("thumbnail cache was not written: %v", err)
	}
	if removed, err := svc.Delete([]string{item.Rel}); err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("thumbnail cache was not deleted: %v", err)
	}
}

func TestSafeRelRejectsTraversal(t *testing.T) {
	if _, err := safeRel("../x.png"); err == nil {
		t.Fatal("expected traversal reject")
	}
	if _, err := safeRel("x.txt"); err == nil {
		t.Fatal("expected extension reject")
	}
}

func TestSaveDetectsImageType(t *testing.T) {
	cfg := config.Default()
	cfg.ImageOutputDir = t.TempDir()
	svc := NewService(cfg)
	item, err := svc.Save([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00})
	if err != nil || filepath.Ext(item.Rel) != ".png" {
		t.Fatalf("item=%#v err=%v", item, err)
	}
	f, _, err := svc.Open(item.Rel)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
