package persistence

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestPostgresDocumentRoundTrip(t *testing.T) {
	url := os.Getenv("IMAGE_POOL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set IMAGE_POOL_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	store, err := OpenPostgres(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := "test:document-roundtrip"
	defer store.Delete(context.Background(), key)
	input := map[string]any{"name": "IMAGE POOL", "nested": map[string]any{"count": 2}}
	if err := store.Save(context.Background(), key, input); err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := store.Load(context.Background(), key, &output); err != nil {
		t.Fatal(err)
	}
	if output["name"] != "IMAGE POOL" {
		t.Fatalf("output=%#v", output)
	}
	health, err := store.Health(context.Background())
	if err != nil || health.Backend != "postgresql" || health.DatabaseURL == "" {
		t.Fatalf("health=%#v err=%v", health, err)
	}
	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(context.Background(), key, &output); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}
