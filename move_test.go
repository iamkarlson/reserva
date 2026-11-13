package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveSession(t *testing.T) {
	root := t.TempDir()
	from := Profile{Name: "a", ConfigDir: filepath.Join(root, "a")}
	to := Profile{Name: "b", ConfigDir: filepath.Join(root, "b")}
	proj := filepath.Join(from.ConfigDir, "projects", "-home-me-repo")
	if err := os.MkdirAll(filepath.Join(proj, "id1"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(proj, "id1.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Session{ID: "id1", Path: path, Profile: "a"}

	if err := moveSession(s, to); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(to.ConfigDir, "projects", "-home-me-repo", "id1.jsonl")
	if s.Path != want || s.Profile != "b" {
		t.Errorf("session now %q in %q, want %q in b", s.Path, s.Profile, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("file not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(to.ConfigDir, "projects", "-home-me-repo", "id1")); err != nil {
		t.Errorf("side directory not moved: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("source still there: %v", err)
	}

	// moving back onto an existing file is refused
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := moveSession(s, from); err == nil {
		t.Error("overwrote an existing session file")
	}
}

func TestCompleteName(t *testing.T) {
	names := []string{"work", "work-cswap", "personal"}
	if got, _ := completeName("p", names); got != "personal" {
		t.Errorf("unique prefix: got %q", got)
	}
	if got, c := completeName("wo", names); got != "wo" || len(c) != 2 {
		t.Errorf("ambiguous prefix: got %q %v", got, c)
	}
	if _, c := completeName("x", names); len(c) != 0 {
		t.Errorf("no match: got %v", c)
	}
}
