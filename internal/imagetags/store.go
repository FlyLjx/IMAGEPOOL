package imagetags

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"imagepool/internal/persistence"
)

type Store struct {
	mu    sync.RWMutex
	path  string
	state persistence.Store
	tags  map[string][]string
}

func New(path string) *Store {
	return newStore(path, nil)
}

func NewWithPersistence(state persistence.Store) *Store {
	return newStore("", state)
}

func newStore(path string, state persistence.Store) *Store {
	s := &Store{path: strings.TrimSpace(path), state: state, tags: map[string][]string{}}
	_ = s.load()
	return s
}

func (s *Store) Get(path string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.tags[cleanPath(path)]...)
}

func (s *Store) All() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	for _, values := range s.tags {
		for _, value := range values {
			seen[value] = true
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (s *Store) Set(path string, values []string) ([]string, error) {
	path = cleanPath(path)
	if path == "" {
		return nil, os.ErrNotExist
	}
	values = cleanTags(values)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(values) == 0 {
		delete(s.tags, path)
	} else {
		s.tags[path] = values
	}
	return values, s.saveLocked()
}

func (s *Store) DeleteTag(tag string) (int, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for path, values := range s.tags {
		next := make([]string, 0, len(values))
		found := false
		for _, value := range values {
			if value == tag {
				found = true
				continue
			}
			next = append(next, value)
		}
		if !found {
			continue
		}
		removed++
		if len(next) == 0 {
			delete(s.tags, path)
		} else {
			s.tags[path] = next
		}
	}
	return removed, s.saveLocked()
}

func (s *Store) RemovePaths(paths []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, path := range paths {
		path = cleanPath(path)
		if _, ok := s.tags[path]; ok {
			delete(s.tags, path)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}

func (s *Store) load() error {
	if s.state != nil {
		var raw map[string][]string
		if err := s.state.Load(context.Background(), "image_tags", &raw); err != nil {
			if errors.Is(err, persistence.ErrNotFound) {
				return nil
			}
			return err
		}
		for path, values := range raw {
			if path = cleanPath(path); path != "" {
				s.tags[path] = cleanTags(values)
			}
		}
		return nil
	}
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var raw map[string][]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for path, values := range raw {
		if path = cleanPath(path); path != "" {
			s.tags[path] = cleanTags(values)
		}
	}
	return nil
}

func (s *Store) saveLocked() error {
	if s.state != nil {
		return s.state.Save(context.Background(), "image_tags", s.tags)
	}
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.tags, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func cleanPath(value string) string {
	return strings.Trim(strings.TrimSpace(strings.ReplaceAll(value, "\\", "/")), "/")
}

func cleanTags(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
