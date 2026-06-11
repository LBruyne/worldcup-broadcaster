package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadJSON(t *testing.T) {
	s := New(t.TempDir())
	in := map[string]string{"hello": "世界杯"}
	if err := s.SaveJSON("2026-06-12", "schedule", in); err != nil {
		t.Fatalf("SaveJSON: %v", err)
	}
	var out map[string]string
	if err := s.LoadJSON("2026-06-12", "schedule", &out); err != nil {
		t.Fatalf("LoadJSON: %v", err)
	}
	if out["hello"] != "世界杯" {
		t.Errorf("roundtrip = %v", out)
	}
}

func TestPushedDedupAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s1 := New(dir)
	if s1.IsPushed("760415", "goal-1") {
		t.Fatal("fresh store should report not pushed")
	}
	if err := s1.MarkPushed("760415", "goal-1"); err != nil {
		t.Fatalf("MarkPushed: %v", err)
	}
	if !s1.IsPushed("760415", "goal-1") {
		t.Fatal("should be pushed after marking")
	}
	if s1.IsPushed("760415", "goal-2") {
		t.Fatal("other key should not be pushed")
	}

	// simulate process restart
	s2 := New(dir)
	if !s2.IsPushed("760415", "goal-1") {
		t.Fatal("pushed state must survive restart")
	}
}

func TestCorruptStateFallsBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "pushed-x.json"), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	if s.IsPushed("x", "k") {
		t.Fatal("corrupt state should fall back to empty, not panic")
	}
}
