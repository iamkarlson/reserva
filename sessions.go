package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

const worktreeMarker = "/.claude/worktrees/"

// Session is one Claude Code conversation found in <config_dir>/projects.
type Session struct {
	ID         string
	Path       string // the .jsonl file
	Profile    string
	CWD        string // directory claude ran in (may be a worktree)
	Repo       string // repo root used for grouping
	Worktree   string // worktree name, when CWD is under .claude/worktrees
	Branch     string
	Title      string
	LastPrompt string
	Modified   time.Time
	Missing    bool     // CWD no longer exists
	Archived   bool     // hidden from the main list (reserva state)
	PRLinks    []string // MR/PR URLs claude recorded for this session
	MRs        []*MR    // open MRs on a forge that belong to this session

	// live state from <config_dir>/sessions/*.json
	Running bool
	Status  string
	PID     int
}

// Group is all sessions that belong to one repository / directory.
type Group struct {
	Repo     string
	Project  string // forge project path, from the origin remote
	Sessions []*Session
	Latest   time.Time
}

type jsonlLine struct {
	Type        string `json:"type"`
	CWD         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	LastPrompt  string `json:"lastPrompt"`
	PRURL       string `json:"prUrl"`
	Message     *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

const chunkSize = 256 * 1024

// readHeadTail returns the first and last chunkSize bytes of a file, cut on
// line boundaries. Small files are returned whole (tail is nil).
func readHeadTail(path string) (head, tail []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if st.Size() <= 2*chunkSize {
		head, err = io.ReadAll(f)
		return head, nil, err
	}
	head = make([]byte, chunkSize)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
		head = head[:i+1]
	}
	tail = make([]byte, chunkSize)
	n, _ = f.ReadAt(tail, st.Size()-chunkSize)
	tail = tail[:n]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return head, tail, nil
}

// promptText extracts the human text from a user message's content.
func promptText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	if raw[0] == '[' {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) != nil {
			return ""
		}
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// firstLine collapses a prompt into a single display line.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "<") {
			continue
		}
		return strings.Join(strings.Fields(l), " ")
	}
	return ""
}

func parseSession(profile, path string) *Session {
	head, tail, err := readHeadTail(path)
	if err != nil {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	s := &Session{
		ID:       strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Path:     path,
		Profile:  profile,
		Modified: st.ModTime(),
	}
	var aiTitle, customTitle, firstUser, lastUser, lastPromptLine string
	scan := func(data []byte) {
		for len(data) > 0 {
			var line []byte
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				line, data = data[:i], data[i+1:]
			} else {
				line, data = data, nil
			}
			// Cheap pre-filter: only decode lines we care about.
			probe := line
			if len(probe) > 200 {
				probe = probe[:200]
			}
			if !bytes.Contains(probe, []byte(`"type":"user"`)) &&
				!bytes.Contains(probe, []byte(`"type":"ai-title"`)) &&
				!bytes.Contains(probe, []byte(`"type":"custom-title"`)) &&
				!bytes.Contains(probe, []byte(`"type":"last-prompt"`)) &&
				!bytes.Contains(probe, []byte(`"type":"pr-link"`)) {
				continue
			}
			var l jsonlLine
			if json.Unmarshal(line, &l) != nil {
				continue
			}
			switch l.Type {
			case "ai-title":
				if l.AITitle != "" {
					aiTitle = l.AITitle
				}
			case "custom-title":
				if l.CustomTitle != "" {
					customTitle = l.CustomTitle
				}
			case "pr-link":
				if l.PRURL != "" && !slices.Contains(s.PRLinks, l.PRURL) {
					s.PRLinks = append(s.PRLinks, l.PRURL)
				}
			case "last-prompt":
				if l.LastPrompt != "" {
					lastPromptLine = firstLine(l.LastPrompt)
				}
			case "user":
				if l.CWD != "" {
					s.CWD = l.CWD
				}
				if l.GitBranch != "" {
					s.Branch = l.GitBranch
				}
				if l.IsMeta || l.IsSidechain || l.Message == nil {
					continue
				}
				if t := firstLine(promptText(l.Message.Content)); t != "" {
					if firstUser == "" {
						firstUser = t
					}
					lastUser = t
				}
			}
		}
	}
	scan(head)
	if tail != nil {
		scan(tail)
	}
	s.Title = customTitle
	if s.Title == "" {
		s.Title = aiTitle
	}
	if s.Title == "" {
		s.Title = firstUser
	}
	s.LastPrompt = lastPromptLine
	if s.LastPrompt == "" {
		s.LastPrompt = lastUser
	}
	if s.CWD == "" {
		return nil
	}
	if s.Title == "" {
		s.Title = s.LastPrompt
	}
	if _, err := os.Stat(s.CWD); err != nil {
		s.Missing = true
	}
	s.Repo, s.Worktree = splitWorktree(s.CWD)
	return s
}

// renameSession appends a custom-title line to the session file, which is
// exactly what claude's /rename and --name do, so claude's own picker shows
// the new name too.
func renameSession(s *Session, title string) error {
	line, err := json.Marshal(struct {
		Type        string `json:"type"`
		CustomTitle string `json:"customTitle"`
		SessionID   string `json:"sessionId"`
	}{"custom-title", title, s.ID})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// splitWorktree maps <repo>/.claude/worktrees/<name>[/...] to (repo, name).
func splitWorktree(cwd string) (repo, name string) {
	if i := strings.Index(cwd, worktreeMarker); i >= 0 {
		rest := cwd[i+len(worktreeMarker):]
		name = rest
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			name = rest[:j]
		}
		return cwd[:i], name
	}
	return cwd, ""
}

type liveSession struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Name      string `json:"name"`
	CWD       string `json:"cwd"`
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// loadLive reads <config_dir>/sessions/*.json and returns the sessions whose
// process is still alive, keyed by session ID.
func loadLive(configDir string) map[string]liveSession {
	out := map[string]liveSession{}
	files, _ := filepath.Glob(filepath.Join(configDir, "sessions", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var ls liveSession
		if json.Unmarshal(data, &ls) != nil || ls.SessionID == "" {
			continue
		}
		if processAlive(ls.PID) {
			out[ls.SessionID] = ls
		}
	}
	return out
}

func applyLive(groups []*Group, live map[string]liveSession) {
	for _, g := range groups {
		for _, s := range g.Sessions {
			ls, ok := live[s.ID]
			s.Running = ok
			s.Status = ls.Status
			s.PID = ls.PID
		}
	}
}

// loadProfile scans every session of a profile and groups them by repo.
func loadProfile(p Profile) ([]*Group, error) {
	files, err := filepath.Glob(filepath.Join(p.ConfigDir, "projects", "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	byRepo := map[string]*Group{}
	for _, f := range files {
		s := parseSession(p.Name, f)
		if s == nil {
			continue
		}
		g := byRepo[s.Repo]
		if g == nil {
			g = &Group{Repo: s.Repo}
			byRepo[s.Repo] = g
		}
		g.Sessions = append(g.Sessions, s)
		if s.Modified.After(g.Latest) {
			g.Latest = s.Modified
		}
	}
	groups := make([]*Group, 0, len(byRepo))
	for _, g := range byRepo {
		sort.Slice(g.Sessions, func(i, j int) bool {
			return g.Sessions[i].Modified.After(g.Sessions[j].Modified)
		})
		g.Project = remoteProject(g.Repo)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Latest.After(groups[j].Latest)
	})
	applyLive(groups, loadLive(p.ConfigDir))
	// Sessions without any prompt are only interesting while they run
	// (a freshly started claude that has not been talked to yet).
	for _, g := range groups {
		kept := g.Sessions[:0]
		for _, s := range g.Sessions {
			if s.Title != "" || s.Running {
				kept = append(kept, s)
			}
		}
		g.Sessions = kept
	}
	return groups, nil
}

// gitRoot walks up from dir to find the main repository root. For a
// worktree it returns the worktree root itself (callers use splitWorktree
// to map claude-managed worktrees back to the main repo).
func gitRoot(dir string) string {
	d := dir
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// moveSession moves a session's file, and the directory of the same name
// next to it when there is one, into the matching projects/ directory of
// another profile. The session then belongs to that profile and is resumed
// with its login; the ID does not change.
func moveSession(s *Session, to Profile) error {
	dir := filepath.Join(expandHome(to.ConfigDir), "projects", filepath.Base(filepath.Dir(s.Path)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, filepath.Base(s.Path))
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	if err := os.Rename(s.Path, dst); err != nil {
		return err
	}
	side := strings.TrimSuffix(s.Path, ".jsonl")
	if st, err := os.Stat(side); err == nil && st.IsDir() {
		if err := os.Rename(side, strings.TrimSuffix(dst, ".jsonl")); err != nil {
			return err
		}
	}
	s.Path = dst
	s.Profile = to.Name
	return nil
}
