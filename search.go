package main

import (
	"bufio"
	"bytes"
	"os"
	"runtime"
	"strings"
	"sync"
	"unicode"

	"github.com/sahilm/fuzzy"
)

// A filter is split into terms on whitespace; every term has to match. A
// term with an upper-case letter is matched case-sensitively, like fzf's
// smart case.
func filterTerms(filter string) []string {
	return strings.Fields(filter)
}

func caseSensitive(term string) bool {
	return strings.IndexFunc(term, unicode.IsUpper) >= 0
}

// fuzzyScore scores term against text with sahilm/fuzzy (Sublime Text style:
// consecutive and word-initial matches score more, unmatched characters
// cost a little). ok is false when term does not match. The matcher always
// ignores case, so a smart-case term must also occur in text, in order,
// exactly as typed.
func fuzzyScore(text, term string) (score int, ok bool) {
	if term == "" {
		return 0, true
	}
	if caseSensitive(term) && !subsequence(text, term) {
		return 0, false
	}
	ms := fuzzy.FindNoSort(term, []string{text})
	if len(ms) == 0 {
		return 0, false
	}
	return ms[0].Score, true
}

// subsequence reports whether the runes of term occur in text in order.
func subsequence(text, term string) bool {
	rest := []rune(term)
	for _, r := range text {
		if len(rest) > 0 && r == rest[0] {
			rest = rest[1:]
		}
	}
	return len(rest) == 0
}

// fuzzyMatch reports whether term fuzzy-matches text.
func fuzzyMatch(text, term string) bool {
	_, ok := fuzzyScore(text, term)
	return ok
}

// titleMatch lifts any fuzzy match on the title above terms that only match
// elsewhere, since fuzzy scores can be negative.
const titleMatch = 1 << 16

// filterScore ranks a session under the filter: the fuzzy scores of the
// terms on its title added up. Terms that only match elsewhere (prompt,
// branch, path, content) add nothing, so the list orders like fzf would.
func filterScore(s *Session, f string) int {
	total := 0
	for _, term := range filterTerms(f) {
		if score, ok := fuzzyScore(s.Title, term); ok {
			total += titleMatch + score
		}
	}
	return total
}

// matchesAny reports whether term fuzzy-matches one of the texts.
func matchesAny(term string, texts ...string) bool {
	for _, t := range texts {
		if fuzzyMatch(t, term) {
			return true
		}
	}
	return false
}

// Content search: enter in the filter prompt scans the session files for
// the terms as plain text, ignoring case. Fuzzy matching on whole
// conversations would let anything through.

// maxSnippets is how many matching lines of a session are kept for the
// preview under the list.
const maxSnippets = 3

// indexFold finds term in line ignoring ASCII case. term is lower-case.
func indexFold(line, term []byte) int {
	if len(term) == 0 {
		return 0
	}
	first := term[0]
	upper := first
	if first >= 'a' && first <= 'z' {
		upper = first - 'a' + 'A'
	}
	for i := 0; i+len(term) <= len(line); i++ {
		if line[i] != first && line[i] != upper {
			continue
		}
		if bytes.EqualFold(line[i:i+len(term)], term) {
			return i
		}
	}
	return -1
}

// lineRole names who wrote a line of the session file: user, assistant, or
// tool for a tool result. The top-level "type" comes before the nested
// message in Claude's files, so the first one is the record's.
func lineRole(line []byte) string {
	const key = `"type":"`
	i := bytes.Index(line, []byte(key))
	if i < 0 {
		return ""
	}
	rest := line[i+len(key):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	role := string(rest[:j])
	if role == "user" && bytes.Contains(line, []byte(`"tool_result"`)) {
		role = "tool"
	}
	return role
}

// snippet cuts the text around a match out of a line of the session file
// (raw JSON, so escapes are undone roughly) for the preview.
func snippet(line []byte, at, n int) string {
	const around = 60
	lo, hi := stringAround(line, at)
	start := max(at-around, lo)
	end := min(at+n+around, hi)
	s := string(line[start:end])
	s = strings.NewReplacer(`\n`, " ", `\t`, " ", `\"`, `"`, `\\`, `\`).Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	if start > lo {
		s = "…" + s
	}
	if end < hi {
		s += "…"
	}
	if role := lineRole(line); role != "" {
		s = role + ": " + s
	}
	return s
}

// stringAround finds the JSON string literal of line that holds position
// at, so a snippet shows the text and not the keys around it. Quotes
// escaped with a backslash do not count.
func stringAround(line []byte, at int) (lo, hi int) {
	lo = at
	for lo > 0 && !(line[lo-1] == '"' && (lo < 2 || line[lo-2] != '\\')) {
		lo--
	}
	hi = at
	for hi < len(line) && !(line[hi] == '"' && line[hi-1] != '\\') {
		hi++
	}
	return lo, hi
}

// scanFile reports whether every term occurs somewhere in the session file
// (prompts, replies and tool output alike) and returns snippets of the
// first lines that mention one. Terms are lower-case.
func scanFile(path string, terms [][]byte) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	found := make([]bool, len(terms))
	left := len(terms)
	var snippets []string
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		for i, t := range terms {
			at := indexFold(line, t)
			if at < 0 {
				continue
			}
			if !found[i] {
				found[i] = true
				left--
			}
			if len(snippets) < maxSnippets {
				snippets = append(snippets, snippet(line, at, len(t)))
			}
			break
		}
		if left == 0 && len(snippets) == maxSnippets {
			return snippets, true
		}
		if err != nil {
			return snippets, left == 0
		}
	}
}

// scanSessions searches the files of the sessions for the terms, in
// parallel, and returns the snippets per matching session ID.
func scanSessions(sessions []*Session, terms []string) map[string][]string {
	lower := make([][]byte, len(terms))
	for i, t := range terms {
		lower[i] = []byte(strings.ToLower(t))
	}
	var mu sync.Mutex
	hits := map[string][]string{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for _, s := range sessions {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if snips, ok := scanFile(s.Path, lower); ok {
				mu.Lock()
				hits[s.ID] = snips
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return hits
}
