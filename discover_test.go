package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverClaudeSwapDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	sessions := filepath.Join(home, ".local", "share", "claude-swap", "sessions")
	for _, d := range []string{"1-georgy_example.com", "2-georgy_work.example", "3-first_last_example.com", "4-work_example.com", "5-empty_example.com"} {
		dir := filepath.Join(sessions, d)
		if d != "5-empty_example.com" {
			dir = filepath.Join(dir, "projects")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	taken := map[string]bool{"default": true, "work": true}
	got := discoverClaudeSwapDirs(taken, nil)
	want := []Profile{
		{Name: "georgy", ConfigDir: "~/.local/share/claude-swap/sessions/1-georgy_example.com"},
		{Name: "georgy-2", ConfigDir: "~/.local/share/claude-swap/sessions/2-georgy_work.example"},
		{Name: "first_last", ConfigDir: "~/.local/share/claude-swap/sessions/3-first_last_example.com"},
		{Name: "work-4", ConfigDir: "~/.local/share/claude-swap/sessions/4-work_example.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].ConfigDir != want[i].ConfigDir {
			t.Errorf("profile %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A ~/.claude-<name> profile and a claude-swap account with the same local
// part must not both become [profiles.<name>]: the config would not parse.
func TestWriteDefaultConfigNameClash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("TERM_PROGRAM", "")
	for _, d := range []string{".claude/projects", ".claude-georgy/projects", ".local/share/claude-swap/sessions/1-georgy_example.com/projects"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := loadConfig(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 3 || cfg.Profiles["georgy"].ConfigDir == "" || cfg.Profiles["georgy-1"].ConfigDir == "" {
		t.Errorf("profiles = %v, want default, georgy and georgy-1", cfg.ProfileNames())
	}
}

func TestTomlKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"work", "work"},
		{"first_last-2", "first_last-2"},
		{"first.last", `"first.last"`},
		{"jo+ci", `"jo+ci"`},
	}
	for _, tt := range tests {
		if got := tomlKey(tt.in); got != tt.want {
			t.Errorf("tomlKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
