package storage

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"imagepool/internal/config"
)

type Service struct{ root string }

type ImageItem struct {
	Rel          string    `json:"rel"`
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	URL          string    `json:"url"`
	ThumbnailURL string    `json:"thumbnail_url,omitempty"`
	Width        int       `json:"width,omitempty"`
	Height       int       `json:"height,omitempty"`
	Size         int64     `json:"size"`
	CreatedAt    time.Time `json:"created_at"`
	Date         string    `json:"date"`
	Local        bool      `json:"local"`
	Storage      string    `json:"storage"`
	Tags         []string  `json:"tags,omitempty"`
}

func NewService(cfg config.Config) *Service { return &Service{root: cfg.Normalize().ImageOutputDir} }

func (s *Service) Root() string { return s.root }

func (s *Service) Save(data []byte) (ImageItem, error) {
	if len(data) == 0 {
		return ImageItem{}, fmt.Errorf("image data is empty")
	}
	root := filepath.Clean(s.root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return ImageItem{}, err
	}
	now := time.Now()
	ext := imageExtension(data)
	sum := sha256.Sum256(data)
	rel := filepath.ToSlash(filepath.Join(now.Format("2006"), now.Format("01"), now.Format("02"), fmt.Sprintf("%d_%x%s", now.UnixNano(), sum[:8], ext)))
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ImageItem{}, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return ImageItem{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return ImageItem{}, err
	}
	width, height := imageDimensions(data)
	return ImageItem{Rel: rel, Path: rel, Name: filepath.Base(path), Width: width, Height: height, Size: int64(len(data)), CreatedAt: now, Date: now.Format("2006-01-02"), Local: true, Storage: "local"}, nil
}

func (s *Service) List(baseURL, startDate, endDate string) ([]ImageItem, error) {
	root := filepath.Clean(s.root)
	items := []ImageItem{}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return err
		}
		if !isImageName(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		day := info.ModTime().Format("2006-01-02")
		if startDate != "" && day < startDate {
			return nil
		}
		if endDate != "" && day > endDate {
			return nil
		}
		width, height := imageDimensionsFromFile(path)
		items = append(items, ImageItem{Rel: rel, Path: rel, Name: d.Name(), URL: strings.TrimRight(baseURL, "/") + "/images/" + url.PathEscape(rel), Width: width, Height: height, Size: info.Size(), CreatedAt: info.ModTime(), Date: day, Local: true, Storage: "local"})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

func (s *Service) Open(rel string) (*os.File, string, error) {
	safe, err := safeRel(rel)
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(filepath.Clean(s.root), filepath.FromSlash(safe))
	rootAbs, _ := filepath.Abs(filepath.Clean(s.root))
	pathAbs, _ := filepath.Abs(path)
	if !strings.HasPrefix(pathAbs, rootAbs+string(os.PathSeparator)) && pathAbs != rootAbs {
		return nil, "", fmt.Errorf("invalid image path")
	}
	f, err := os.Open(pathAbs)
	return f, filepath.Base(pathAbs), err
}

func (s *Service) Delete(paths []string) (int, error) {
	removed := 0
	for _, rel := range paths {
		f, _, err := s.Open(rel)
		if err == nil {
			name := f.Name()
			f.Close()
			if os.Remove(name) == nil {
				removed++
			}
		}
	}
	return removed, nil
}

func (s *Service) Zip(paths []string) (*bytes.Reader, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	for _, rel := range paths {
		f, name, err := s.Open(rel)
		if err != nil {
			continue
		}
		w, err := zw.Create(name)
		if err == nil {
			_, _ = io.Copy(w, f)
		}
		f.Close()
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}

func (s *Service) Stats() map[string]any {
	items, _ := s.List("", "", "")
	var size int64
	for _, item := range items {
		size += item.Size
	}
	imageMB := float64(size) / (1024 * 1024)
	total, used, free, _ := diskUsage(s.root)
	return map[string]any{"root": s.root, "count": len(items), "image_count": len(items), "bytes": size, "image_size_bytes": size, "image_size_mb": imageMB, "disk_total_mb": bytesToMB(total), "disk_used_mb": bytesToMB(used), "disk_free_mb": bytesToMB(free), "backend": "local"}
}

// Compress rewrites PNG and JPEG files only when the replacement is smaller.
func (s *Service) Compress() (compressed int, savedBytes int64, err error) {
	items, err := s.List("", "", "")
	if err != nil {
		return 0, 0, err
	}
	for _, item := range items {
		data, readErr := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(item.Rel)))
		if readErr != nil {
			continue
		}
		encoded, ok := compressedImage(data, strings.ToLower(filepath.Ext(item.Name)))
		if !ok || len(encoded) >= len(data) {
			continue
		}
		path := filepath.Join(s.root, filepath.FromSlash(item.Rel))
		tmp := path + ".tmp"
		if writeErr := os.WriteFile(tmp, encoded, 0o600); writeErr != nil {
			continue
		}
		if renameErr := os.Rename(tmp, path); renameErr != nil {
			_ = os.Remove(tmp)
			continue
		}
		compressed++
		savedBytes += int64(len(data) - len(encoded))
	}
	return compressed, savedBytes, nil
}

// CleanupToFreeMB removes the oldest cached images until the requested free space is reached.
func (s *Service) CleanupToFreeMB(targetMB int64) (removed int, freedBytes int64, removedPaths []string, done bool, err error) {
	if targetMB <= 0 {
		return 0, 0, nil, false, fmt.Errorf("target_free_mb must be greater than zero")
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return 0, 0, nil, false, err
	}
	_, _, free, err := diskUsage(s.root)
	if err != nil {
		return 0, 0, nil, false, err
	}
	targetBytes := targetMB * 1024 * 1024
	if free >= targetBytes {
		return 0, 0, nil, true, nil
	}
	items, err := s.List("", "", "")
	if err != nil {
		return 0, 0, nil, false, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	for _, item := range items {
		path := filepath.Join(s.root, filepath.FromSlash(item.Rel))
		if removeErr := os.Remove(path); removeErr != nil {
			continue
		}
		removed++
		freedBytes += item.Size
		removedPaths = append(removedPaths, item.Rel)
		free += item.Size
		if free >= targetBytes {
			return removed, freedBytes, removedPaths, true, nil
		}
	}
	return removed, freedBytes, removedPaths, false, nil
}

func imageDimensionsFromFile(path string) (int, int) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err == nil && config.Width > 0 && config.Height > 0 {
		return config.Width, config.Height
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, 0
	}
	header := make([]byte, 64)
	read, _ := io.ReadFull(file, header)
	if read == 0 {
		return 0, 0
	}
	return webpDimensions(header[:read])
}

func imageDimensions(data []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err == nil && cfg.Width > 0 && cfg.Height > 0 {
		return cfg.Width, cfg.Height
	}
	return webpDimensions(data)
}

func webpDimensions(data []byte) (int, int) {
	if len(data) < 30 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0
	}
	switch string(data[12:16]) {
	case "VP8X":
		if len(data) < 30 {
			return 0, 0
		}
		width := 1 + int(data[24]) + (int(data[25]) << 8) + (int(data[26]) << 16)
		height := 1 + int(data[27]) + (int(data[28]) << 8) + (int(data[29]) << 16)
		return width, height
	case "VP8L":
		if len(data) < 25 || data[20] != 0x2f {
			return 0, 0
		}
		b1, b2, b3, b4 := data[21], data[22], data[23], data[24]
		width := 1 + int(b1) + (int(b2&0x3f) << 8)
		height := 1 + (int(b2&0xc0) >> 6) + (int(b3) << 2) + (int(b4&0x0f) << 10)
		return width, height
	case "VP8 ":
		if len(data) < 30 || data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0
		}
		width := int(data[26]) + (int(data[27]&0x3f) << 8)
		height := int(data[28]) + (int(data[29]&0x3f) << 8)
		return width, height
	}
	return 0, 0
}

func compressedImage(data []byte, extension string) ([]byte, bool) {
	if extension != ".png" && extension != ".jpg" && extension != ".jpeg" {
		return nil, false
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	buffer := new(bytes.Buffer)
	switch extension {
	case ".png":
		encoder := png.Encoder{CompressionLevel: png.BestCompression}
		err = encoder.Encode(buffer, decoded)
	default:
		err = jpeg.Encode(buffer, decoded, &jpeg.Options{Quality: 85})
	}
	if err != nil {
		return nil, false
	}
	return buffer.Bytes(), true
}

func bytesToMB(value int64) float64 { return float64(max64(0, value)) / (1024 * 1024) }

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func safeRel(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "/")
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("invalid path")
	}
	if !isImageName(clean) {
		return "", fmt.Errorf("not an image")
	}
	return clean, nil
}

func isImageName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	}
	return false
}

func imageExtension(data []byte) string {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return ".webp"
	}
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		return ".png"
	}
	if len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}) {
		return ".jpg"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return ".gif"
	}
	return ".png"
}
