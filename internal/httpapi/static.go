package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type staticFiles struct{ root string }

func newStaticFiles(root string) *staticFiles {
	root = strings.TrimSpace(root)
	if root == "" {
		return &staticFiles{}
	}
	return &staticFiles{root: filepath.Clean(root)}
}

func (s *staticFiles) Serve(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || s.root == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) || apiPath(r.URL.Path) {
		return false
	}
	root, err := filepath.Abs(s.root)
	if err != nil {
		return false
	}
	rel := strings.TrimPrefix(strings.ReplaceAll(r.URL.Path, "\\", "/"), "/")
	if rel == "" {
		rel = "index.html"
	}
	candidates := []string{rel}
	if !strings.HasSuffix(rel, ".html") && !strings.Contains(filepath.Base(rel), ".") {
		candidates = append(candidates, filepath.ToSlash(filepath.Join(rel, "index.html")), rel+".html")
	}
	for _, candidate := range candidates {
		if path := s.resolve(root, candidate); path != "" && isFile(path) {
			http.ServeFile(w, r, path)
			return true
		}
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") {
		if path := s.resolve(root, "index.html"); path != "" && isFile(path) {
			http.ServeFile(w, r, path)
			return true
		}
	}
	return false
}

func (s *staticFiles) resolve(root, rel string) string {
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return path
	}
	return ""
}

func apiPath(path string) bool {
	return path == "/api" || path == "/v1" || path == "/auth" || strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/auth/")
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
