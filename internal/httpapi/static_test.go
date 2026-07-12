package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticFilesServesRoutesAndDoesNotStealAPI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("home"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "login"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "login", "index.html"), []byte("login"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := newStaticFiles(root)
	for _, test := range []struct{ path, want string }{{"/", "home"}, {"/login/", "login"}, {"/dashboard/", "home"}} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		req.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		if !files.Serve(response, req) || response.Body.String() != test.want {
			t.Fatalf("path=%s status=%d body=%q", test.path, response.Code, response.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	if files.Serve(httptest.NewRecorder(), req) {
		t.Fatal("api path served as static file")
	}
}
