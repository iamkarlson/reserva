package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		text, term string
		want       bool
	}{
		{"Fix flaky login test", "flt", true},
		{"Fix flaky login test", "FLT", false}, // smart case: upper case must match as typed
		{"Fix flaky login test", "Flt", true},
		{"Fix flaky login test", "zz", false},
		{"Refactor API client", "apicl", true},
		{"Refactor API client", "API", true},
		{"", "a", false},
		{"anything", "", true},
	}
	for _, tt := range tests {
		if got := fuzzyMatch(tt.text, tt.term); got != tt.want {
			t.Errorf("fuzzyMatch(%q, %q) = %v, want %v", tt.text, tt.term, got, tt.want)
		}
	}
}

func TestFilterScore(t *testing.T) {
	tight := &Session{Title: "login test"}
	loose := &Session{Title: "long ongoing integration test"}
	elsewhere := &Session{Title: "unrelated", Branch: "login"}
	if a, b := filterScore(tight, "login"), filterScore(loose, "login"); a <= b {
		t.Errorf("consecutive match scored %d, scattered one %d; want the first higher", a, b)
	}
	if a, b := filterScore(loose, "login"), filterScore(elsewhere, "login"); a <= b {
		t.Errorf("title match scored %d, branch-only match %d; want the title higher", a, b)
	}
}

func TestMatches(t *testing.T) {
	s := &Session{ID: "abc123", Title: "Fix flaky login test", Branch: "fix/login", Repo: "/src/webapp"}
	tests := []struct {
		filter string
		want   bool
	}{
		{"flt", true},
		{"flt webapp", true},  // fuzzy title and path substring
		{"flt mobile", false}, // every term has to match
		{"fix/log", true},
		{"abc", true},
		{"bc12", false}, // IDs match by prefix only
	}
	for _, tt := range tests {
		if got := matches(s, tt.filter); got != tt.want {
			t.Errorf("matches(%q) = %v, want %v", tt.filter, got, tt.want)
		}
	}
}

func TestScanFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"Please fix the KeePass sync"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Looking at keepass.go now"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"keepass.go:12: undefined: Vault"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Found it in the sync loop"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snips, ok := scanFile(path, [][]byte{[]byte("keepass"), []byte("sync")})
	if !ok {
		t.Fatal("both terms occur, want a hit")
	}
	want := []string{
		"user: Please fix the KeePass sync",
		"assistant: Looking at keepass.go now",
		"tool: keepass.go:12: undefined: Vault",
	}
	if len(snips) != len(want) {
		t.Fatalf("got %d snippets %q, want %d", len(snips), snips, len(want))
	}
	for i := range want {
		if !strings.Contains(snips[i], want[i]) {
			t.Errorf("snippet %d = %q, want it to contain %q", i, snips[i], want[i])
		}
	}
	if _, ok := scanFile(path, [][]byte{[]byte("keepass"), []byte("missing")}); ok {
		t.Error("one term is missing, want no hit")
	}
	if _, ok := scanFile(filepath.Join(t.TempDir(), "none.jsonl"), [][]byte{[]byte("x")}); ok {
		t.Error("unreadable file, want no hit")
	}
}

func TestLineRole(t *testing.T) {
	tests := []struct{ line, want string }{
		{`{"parentUuid":null,"type":"user","message":{"role":"user","content":"hi"}}`, "user"},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"x"}]}}`, "assistant"},
		{`{"type":"user","message":{"content":[{"type":"tool_result","content":"out"}]}}`, "tool"},
		{`not json`, ""},
	}
	for _, tt := range tests {
		if got := lineRole([]byte(tt.line)); got != tt.want {
			t.Errorf("lineRole(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}
