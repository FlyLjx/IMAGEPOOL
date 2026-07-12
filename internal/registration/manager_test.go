package registration

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"imagepool/internal/accounts"
)

func TestManagerRunsWorkersPersistsAndAddsAccounts(t *testing.T) {
	store := accounts.NewStore(nil, "")
	worker := func(_ context.Context, _ Config, index int) (accounts.Account, error) {
		if index == 2 {
			return accounts.Account{}, fmt.Errorf("expected failure")
		}
		return accounts.Account{AccessToken: fmt.Sprintf("token-%d", index), Email: fmt.Sprintf("user-%d@example.test", index), Status: "正常"}, nil
	}
	path := filepath.Join(t.TempDir(), "register.json")
	manager := NewManager(path, store, worker)
	manager.Update(map[string]any{"total": 3, "threads": 2})
	manager.Start()
	deadline := time.Now().Add(2 * time.Second)
	for manager.Get().Enabled && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	config := manager.Get()
	if config.Enabled || config.Stats.Done != 3 || config.Stats.Success != 2 || config.Stats.Fail != 1 || len(store.List()) != 2 {
		t.Fatalf("config=%#v accounts=%#v", config, store.List())
	}
	if reloaded := NewManager(path, store, worker).Get(); reloaded.Total != 3 || reloaded.Threads != 2 || len(reloaded.Logs) == 0 {
		t.Fatalf("reloaded=%#v", reloaded)
	}
}

func TestManagerWithoutWorkerDoesNotStart(t *testing.T) {
	manager := NewManager("", accounts.NewStore(nil, ""), nil)
	config := manager.Start()
	if config.Enabled || len(config.Logs) != 1 || config.Logs[0].Level != "red" {
		t.Fatalf("config=%#v", config)
	}
}

func TestManagerStopsWhenAvailableTargetReached(t *testing.T) {
	store := accounts.NewStore(nil, "")
	manager := NewManager("", store, func(_ context.Context, _ Config, index int) (accounts.Account, error) {
		return accounts.Account{AccessToken: fmt.Sprintf("available-%d", index), Status: "正常"}, nil
	})
	manager.Update(map[string]any{"mode": "available", "target_available": 1, "total": 99, "threads": 1})
	manager.Start()
	deadline := time.Now().Add(2 * time.Second)
	for manager.Get().Enabled && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	config := manager.Get()
	if config.Stats.Success != 1 || config.Stats.Done != 1 || len(store.List()) != 1 {
		t.Fatalf("config=%#v accounts=%#v", config, store.List())
	}
}
