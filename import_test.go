package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportClaudeSwap(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	known := filepath.Join(data, "claude-swap", "sessions", "1-me_work.com")
	fresh := filepath.Join(data, "claude-swap", "sessions", "2-me_home.org")
	for _, d := range []string{known, fresh} {
		if err := os.MkdirAll(filepath.Join(d, "projects"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	body := "# my comment\n[profiles.work]\nconfig_dir = \"" + known + "\"\n"
	cfg, err := loadConfig(writeTestConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	run := func(force bool, answer string) (*Config, string) {
		t.Helper()
		var out bytes.Buffer
		var in io.Reader
		if answer != "" {
			in = strings.NewReader(answer)
		}
		got, err := importClaudeSwap(cfg, force, in, &out)
		if err != nil {
			t.Fatal(err)
		}
		return got, out.String()
	}

	// Nobody to ask: nothing is asked and nothing is added.
	if got, out := run(false, ""); len(got.Profiles) != 1 || out != "" {
		t.Errorf("without a terminal: %d profiles, output %q", len(got.Profiles), out)
	}
	// Declined: nothing is added, the flag is named, and it does not ask again.
	got, out := run(false, "n\n")
	if len(got.Profiles) != 1 || !strings.Contains(out, "--import-cswap") {
		t.Errorf("declined: %d profiles, output %q", len(got.Profiles), out)
	}
	if _, out := run(false, "y\n"); out != "" {
		t.Errorf("asked again after a decline: %q", out)
	}
	// The flag adds the account that is not a profile yet, and only that one.
	got, _ = run(true, "")
	if len(got.Profiles) != 2 || got.Profiles["me"].ConfigDir != fresh {
		t.Errorf("--import-cswap: profiles = %v", got.Profiles)
	}
	text, _ := os.ReadFile(got.path)
	if !strings.HasPrefix(string(text), body) {
		t.Errorf("the config file was rewritten:\n%s", text)
	}
	cfg = got
	if _, out := run(true, ""); !strings.Contains(out, "no claude-swap accounts") {
		t.Errorf("second import: %q", out)
	}
}
