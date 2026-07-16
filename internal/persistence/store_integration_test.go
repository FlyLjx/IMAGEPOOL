package persistence

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
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

	collection := "test:collection-roundtrip"
	defer store.DeleteCollection(context.Background(), collection)
	if err := store.SaveCollectionItems(context.Background(), collection, map[string]any{
		"a": map[string]any{"id": "a", "value": 1},
		"b": map[string]any{"id": "b", "value": 2},
	}); err != nil {
		t.Fatal(err)
	}
	var collectionOutput []map[string]any
	if err := store.LoadCollection(context.Background(), collection, &collectionOutput); err != nil {
		t.Fatal(err)
	}
	if len(collectionOutput) != 2 {
		t.Fatalf("collection=%#v", collectionOutput)
	}
	if err := store.SaveCollectionItems(context.Background(), collection, map[string]any{
		"b": map[string]any{"id": "b", "value": 3},
	}); err != nil {
		t.Fatal(err)
	}
	collectionOutput = nil
	if err := store.LoadCollection(context.Background(), collection, &collectionOutput); err != nil {
		t.Fatal(err)
	}
	values := map[string]int{}
	for _, item := range collectionOutput {
		values[item["id"].(string)] = int(item["value"].(float64))
	}
	if values["a"] != 1 || values["b"] != 3 {
		t.Fatalf("collection values=%#v", values)
	}
	var windowOutput []map[string]any
	if err := store.LoadCollectionWindow(context.Background(), collection, CollectionWindow{UpdatedSince: time.Now().Add(-time.Hour), CompletedLimit: 10, ActiveStatuses: []string{"running"}}, &windowOutput); err != nil {
		t.Fatal(err)
	}
	if len(windowOutput) != 2 {
		t.Fatalf("collection window=%#v", windowOutput)
	}
	var pageOutput []map[string]any
	total, err := store.LoadCollectionPage(context.Background(), collection, CollectionPage{Limit: 1, Offset: 1, AllowAll: true}, &pageOutput)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(pageOutput) != 1 {
		t.Fatalf("total=%d page=%#v", total, pageOutput)
	}
	if err := store.DeleteCollectionItems(context.Background(), collection, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	collectionOutput = nil
	if err := store.LoadCollection(context.Background(), collection, &collectionOutput); err != nil {
		t.Fatal(err)
	}
	if len(collectionOutput) != 1 || collectionOutput[0]["id"] != "b" {
		t.Fatalf("collection after item delete=%#v", collectionOutput)
	}
	if err := store.DeleteCollection(context.Background(), collection); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadCollection(context.Background(), collection, &collectionOutput); !errors.Is(err, ErrNotFound) {
		t.Fatalf("collection delete err=%v", err)
	}
}
