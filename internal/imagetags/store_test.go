package imagetags

import (
	"path/filepath"
	"testing"
)

func TestSetListDeleteAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tags.json")
	store := New(path)
	tags, err := store.Set("2026/07/a.png", []string{"design", "design", "favorite"})
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags=%#v err=%v", tags, err)
	}
	if all := store.All(); len(all) != 2 {
		t.Fatalf("all=%#v", all)
	}
	removed, err := store.DeleteTag("design")
	if err != nil || removed != 1 || len(New(path).Get("2026/07/a.png")) != 1 {
		t.Fatalf("removed=%d err=%v tags=%#v", removed, err, New(path).Get("2026/07/a.png"))
	}
}
