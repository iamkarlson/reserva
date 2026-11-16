package main

import (
	"strings"
	"testing"
)

func TestFooterKeys(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := &Config{AllTab: "first", Profiles: map[string]Profile{"a": {Name: "a", ConfigDir: t.TempDir()}}, order: []string{"a"}, DefaultProfile: "a"}
	m := newModel(cfg, "a")

	wide := m.keysView(200)
	for _, want := range []string{"rename", "sort", "fold", "help", "quit"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide footer misses %q: %s", want, wide)
		}
	}
	narrow := m.keysView(90)
	for _, want := range []string{"claude", "filter", "help", "quit"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("narrow footer must keep %q: %s", want, narrow)
		}
	}
	if strings.Contains(narrow, "fold") {
		t.Errorf("narrow footer must drop the extra keys first: %s", narrow)
	}
}
