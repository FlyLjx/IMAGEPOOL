package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"imagepool/internal/persistence"
)

type source struct {
	key  string
	path string
}

func main() {
	databaseURL := flag.String("database-url", "", "PostgreSQL connection URL")
	accountsPath := flag.String("accounts", "", "accounts JSON file")
	callsPath := flag.String("calls", "", "calls JSON file")
	authPath := flag.String("auth-keys", "", "auth keys JSON file")
	tagsPath := flag.String("image-tags", "", "image tags JSON file")
	registrationPath := flag.String("registration", "", "registration JSON file")
	flag.Parse()

	if strings.TrimSpace(*databaseURL) == "" {
		log.Fatal("-database-url is required")
	}
	state, err := persistence.OpenPostgres(context.Background(), *databaseURL)
	if err != nil {
		log.Fatalf("open PostgreSQL: %v", err)
	}
	defer state.Close()

	for _, item := range []source{
		{key: "accounts", path: *accountsPath},
		{key: "calls", path: *callsPath},
		{key: "auth_keys", path: *authPath},
		{key: "image_tags", path: *tagsPath},
		{key: "registration", path: *registrationPath},
	} {
		if err := migrateFile(context.Background(), state, item); err != nil {
			log.Fatal(err)
		}
	}
}

func migrateFile(ctx context.Context, state persistence.Store, item source) error {
	path := strings.TrimSpace(item.path)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if !json.Valid(data) {
		return fmt.Errorf("invalid JSON in %s", path)
	}
	if err := state.Save(ctx, item.key, json.RawMessage(data)); err != nil {
		return fmt.Errorf("save %s: %w", item.key, err)
	}
	log.Printf("migrated %s from %s", item.key, filepath.Clean(path))
	return nil
}
