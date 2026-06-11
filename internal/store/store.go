// Package store persists fetched data snapshots and pushed-event state so
// restarts never re-push events that already reached the group.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Store struct {
	dir string

	mu     sync.Mutex
	pushed map[string]map[string]bool // matchID -> eventKey -> true
}

func New(dir string) *Store {
	return &Store{dir: dir, pushed: make(map[string]map[string]bool)}
}

// SaveJSON writes v to <dir>/<date>/<name>.json atomically (write+rename).
func (s *Store) SaveJSON(date, name string, v any) error {
	dir := filepath.Join(s.dir, date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return atomicWrite(filepath.Join(dir, name+".json"), v)
}

// LoadJSON reads <dir>/<date>/<name>.json into v.
func (s *Store) LoadJSON(date, name string, v any) error {
	raw, err := os.ReadFile(filepath.Join(s.dir, date, name+".json"))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (s *Store) stateFile(matchID string) string {
	return filepath.Join(s.dir, "state", "pushed-"+matchID+".json")
}

func (s *Store) loadStateLocked(matchID string) map[string]bool {
	if m, ok := s.pushed[matchID]; ok {
		return m
	}
	m := make(map[string]bool)
	if raw, err := os.ReadFile(s.stateFile(matchID)); err == nil {
		_ = json.Unmarshal(raw, &m) // corrupt state file falls back to empty
	}
	s.pushed[matchID] = m
	return m
}

// IsPushed reports whether eventKey of matchID has already been broadcast.
func (s *Store) IsPushed(matchID, eventKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadStateLocked(matchID)[eventKey]
}

// MarkPushed records eventKey as broadcast and persists the state file.
func (s *Store) MarkPushed(matchID, eventKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.loadStateLocked(matchID)
	m[eventKey] = true
	if err := os.MkdirAll(filepath.Join(s.dir, "state"), 0o755); err != nil {
		return err
	}
	return atomicWrite(s.stateFile(matchID), m)
}

func atomicWrite(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
