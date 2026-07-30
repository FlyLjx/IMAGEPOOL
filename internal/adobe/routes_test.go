package adobe

import "testing"

func TestNormalizeRouteURLSupportsDirectAndProxy(t *testing.T) {
	value, kind, err := normalizeRouteURL("direct")
	if err != nil || value != directRouteURL || kind != "direct" {
		t.Fatalf("direct value=%q kind=%q err=%v", value, kind, err)
	}
	value, kind, err = normalizeRouteURL("http://user:pass@proxy.example:8080")
	if err != nil || value == "" || kind != "proxy" {
		t.Fatalf("proxy value=%q kind=%q err=%v", value, kind, err)
	}
	if _, _, err := normalizeRouteURL(""); err == nil {
		t.Fatal("empty route was accepted")
	}
}
