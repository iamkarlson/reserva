package main

import (
	"strings"
	"testing"
)

func TestHelpScroll(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := &Config{AllTab: "first", Profiles: map[string]Profile{"a": {Name: "a", ConfigDir: t.TempDir()}}, order: []string{"a"}, DefaultProfile: "a"}
	m := newModel(cfg, "a")
	m.width, m.height = 120, 20
	m.mode = modeHelp
	all := m.helpLines()
	last := all[len(all)-1]
	if h := m.listHeight(); len(all) <= h {
		t.Fatalf("help has %d lines, fits in %d: nothing to scroll", len(all), h)
	}

	view := m.helpView()
	if strings.Contains(view, last) || !strings.Contains(view, "more lines") {
		t.Errorf("top of a cut help must say that lines follow:\n%s", view)
	}
	m.helpTop = len(all) // past the end: clamped to the last page
	view = m.helpView()
	if !strings.Contains(view, last) || strings.Contains(view, "more lines") {
		t.Errorf("bottom of the help must end with its last line:\n%s", view)
	}
	if n := strings.Count(view, "\n"); n != m.listHeight() {
		t.Errorf("help view has %d lines, want %d", n, m.listHeight())
	}
}
