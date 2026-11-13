package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type mode int

const (
	modeList mode = iota
	modeFilter
	modeRepo
	modeName
	modeBranch
	modeRename
	modeMove
	modeHelp
)

type row struct {
	group *Group
	sess  *Session // nil for a group header and for MRs without a session
	mr    *MR      // set for an MR row in the MR view
	n     int      // count shown on a group header
}

func (r row) header() bool { return r.sess == nil && r.mr == nil }

// unlinkedGroup heads the open MRs that no visible session claims.
var unlinkedGroup = &Group{}

// allProfile is the name of the tab that shows every profile's sessions
// together. It exists when there is more than one profile.
const allProfile = "all"

type profileData struct {
	groups  []*Group
	loaded  bool
	loading bool
	err     error
}

type loadedMsg struct {
	profile string
	groups  []*Group
	err     error
}

type liveMsg struct{}

type rescanMsg struct{}

// scanMsg carries the result of a content search: snippets per session ID
// for the sessions whose files mention every term of the filter.
type scanMsg struct {
	profile string
	filter  string
	hits    map[string][]string
}

type forgeTickMsg struct{}

type mrsMsg struct {
	profile string
	mrs     []*MR
	err     error
}

type execDoneMsg struct {
	name    string
	cmdline string
	stderr  string
	err     error
}

type model struct {
	cfg        *Config
	profiles   []string
	profile    int
	data       map[string]*profileData
	live       map[string]map[string]liveSession // live sessions per profile, for the tab counts
	rows       []row
	cursor     int
	offset     int
	width      int
	height     int
	mode       mode
	input      string
	filter     string
	hits       map[string][]string // content search results per session ID, nil = not searched
	scanning   bool
	status     string
	lastKey    string
	launchWT   bool     // pending new-session: use a worktree
	launchIn   string   // pending new-session: repo dir
	launchName string   // pending new-session: worktree / session name
	kbOK       bool     // terminal reports shift+enter / ctrl+enter
	compl      []string // completion candidates shown in the repo prompt

	archive      *archiveState
	showArchived bool

	sortBy   int  // index into sortKeys
	sortDesc bool // reverse the key's natural order

	// depth is how many levels under a project header are shown, keyed by
	// repository: 0 is folded, absent is fully open. The session list has
	// one level (sessions), the MR view two (sessions, then their MRs).
	depth map[string]int

	mrView  bool             // show only sessions with open MRs, MRs nested
	mrs     map[string][]*MR // open MRs per profile
	mrErr   map[string]error // last forge error per profile
	mrBusy  map[string]bool  // a forge fetch is in flight
	mrCache *mrCache
}

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2"))
	styleTab      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleTabOn    = lipgloss.NewStyle().Bold(true).Reverse(true)
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	styleHeaderOn = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4")).Reverse(true)
	styleRow      = lipgloss.NewStyle()
	styleRowOn    = lipgloss.NewStyle().Reverse(true)
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleBusy     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleIdle     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleKey      = lipgloss.NewStyle().Bold(true)
	styleRule     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleMR       = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)

func newModel(cfg *Config, startProfile string) model {
	m := model{
		cfg:      cfg,
		profiles: cfg.ProfileNames(),
		data:     map[string]*profileData{},
		live:     map[string]map[string]liveSession{},
		archive:  loadArchive(defaultStatePath()),
		mrs:      map[string][]*MR{},
		mrErr:    map[string]error{},
		mrBusy:   map[string]bool{},
		mrCache:  loadMRCache(defaultMRCachePath()),
		sortBy:   slices.Index(sortKeys, cfg.Sort),
		depth:    map[string]int{},
	}
	for p, mrs := range m.mrCache.Profiles {
		if len(cfg.Profiles[p].Forges) > 0 {
			m.mrs[p] = mrs
		}
	}
	for _, p := range m.profiles {
		m.data[p] = &profileData{}
		m.live[p] = loadLive(cfg.Profiles[p].ConfigDir)
	}
	if len(m.profiles) > 1 && cfg.Profiles[allProfile].ConfigDir == "" {
		if cfg.AllTab == "last" {
			m.profiles = append(m.profiles, allProfile)
		} else {
			m.profiles = append([]string{allProfile}, m.profiles...)
		}
		m.data[allProfile] = &profileData{}
	}
	for i, p := range m.profiles {
		if p == startProfile {
			m.profile = i
		}
	}
	return m
}

// all reports whether the combined tab is selected.
func (m model) all() bool { return m.profiles[m.profile] == allProfile }

// current is the profile new sessions are started in: the selected one, or
// the default profile on the combined tab.
func (m model) current() Profile {
	if m.all() {
		return m.cfg.Profiles[m.cfg.DefaultProfile]
	}
	return m.cfg.Profiles[m.profiles[m.profile]]
}

// profileOf is the profile a session belongs to, which is what resuming it
// must use even on the combined tab.
func (m model) profileOf(s *Session) Profile {
	if s == nil {
		return m.current()
	}
	return m.cfg.Profiles[s.Profile]
}

func (m model) pd() *profileData { return m.data[m.profiles[m.profile]] }

// realProfiles are the tabs that are config profiles, without the all tab.
func (m model) realProfiles() []string {
	if _, ok := m.data[allProfile]; !ok {
		return m.profiles
	}
	return slices.DeleteFunc(slices.Clone(m.profiles), func(p string) bool { return p == allProfile })
}

func (m model) loadCmd(name string) tea.Cmd {
	if name == allProfile {
		return m.reloadCmd()
	}
	p := m.cfg.Profiles[name]
	return func() tea.Msg {
		groups, err := loadProfile(p)
		return loadedMsg{profile: name, groups: groups, err: err}
	}
}

// reloadCmd loads the selected profile, or every profile on the all tab,
// skipping the ones already being loaded.
func (m model) reloadCmd() tea.Cmd {
	names := []string{m.profiles[m.profile]}
	if m.all() {
		names = m.realProfiles()
	}
	return m.loadNames(names...)
}

// loadNames loads the named profiles, skipping the ones already loading.
func (m model) loadNames(names ...string) tea.Cmd {
	var cmds []tea.Cmd
	for _, name := range names {
		pd := m.data[name]
		if pd.loading {
			continue
		}
		pd.loading = true
		cmds = append(cmds, m.loadCmd(name))
	}
	return tea.Batch(cmds...)
}

// mergeAll rebuilds the all tab from the profiles' groups. Sessions are
// shared, so live state, MRs and archiving applied per profile show up here
// too; only the groups are new, since one repository can have sessions in
// several profiles.
func (m *model) mergeAll() {
	pd, ok := m.data[allProfile]
	if !ok {
		return
	}
	pd.loaded, pd.loading, pd.err = true, false, nil
	byRepo := map[string]*Group{}
	for _, name := range m.realProfiles() {
		src := m.data[name]
		if !src.loaded {
			pd.loaded = false
		}
		if src.loading {
			pd.loading = true
		}
		if src.err != nil && pd.err == nil {
			pd.err = src.err
		}
		for _, g := range src.groups {
			dst := byRepo[g.Repo]
			if dst == nil {
				dst = &Group{Repo: g.Repo, Project: g.Project}
				byRepo[g.Repo] = dst
			}
			dst.Sessions = append(dst.Sessions, g.Sessions...)
			if g.Latest.After(dst.Latest) {
				dst.Latest = g.Latest
			}
		}
	}
	pd.groups = pd.groups[:0]
	for _, g := range byRepo {
		sort.Slice(g.Sessions, func(i, j int) bool {
			return g.Sessions[i].Modified.After(g.Sessions[j].Modified)
		})
		pd.groups = append(pd.groups, g)
	}
	sort.Slice(pd.groups, func(i, j int) bool {
		return pd.groups[i].Latest.After(pd.groups[j].Latest)
	})
}

// currentMRs are the open MRs of the selected profile, or of all of them.
func (m model) currentMRs() []*MR {
	if !m.all() {
		return m.mrs[m.profiles[m.profile]]
	}
	var out []*MR
	for _, p := range m.realProfiles() {
		out = append(out, m.mrs[p]...)
	}
	return out
}

func liveTick() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return liveMsg{} })
}

func rescanTick() tea.Cmd {
	return tea.Tick(10*time.Second, func(time.Time) tea.Msg { return rescanMsg{} })
}

func forgeTick() tea.Cmd {
	return tea.Tick(2*time.Minute, func(time.Time) tea.Msg { return forgeTickMsg{} })
}

// fetchMRsCmd asks a profile's forges for its open MRs in the background.
func (m model) fetchMRsCmd(name string) tea.Cmd {
	forges := m.cfg.Profiles[name].Forges
	if len(forges) == 0 || m.mrBusy[name] {
		return nil
	}
	m.mrBusy[name] = true
	return func() tea.Msg {
		mrs, err := fetchOpenMRs(forges)
		return mrsMsg{profile: name, mrs: mrs, err: err}
	}
}

func (m model) fetchAllMRsCmd() tea.Cmd {
	var cmds []tea.Cmd
	for _, p := range m.profiles {
		cmds = append(cmds, m.fetchMRsCmd(p))
	}
	return tea.Batch(cmds...)
}

func (m model) hasForges() bool {
	for _, p := range m.profiles {
		if len(m.cfg.Profiles[p].Forges) > 0 {
			return true
		}
	}
	return false
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.reloadCmd(), liveTick(), rescanTick(), m.fetchAllMRsCmd(), forgeTick())
}

// sortKeys are the orders the list can be in: by recent activity (newest
// first), or by repository path and then session directory, title or branch.
var sortKeys = []string{"activity", "path", "name", "branch"}

// sortGroups orders the groups and the sessions in them by the selected key.
// Ties fall back to recent activity. S reverses the key's order.
func (m *model) sortGroups(groups []*Group) {
	key := sortKeys[m.sortBy]
	less := func(c int) bool {
		if m.sortDesc {
			return c > 0
		}
		return c < 0
	}
	sessionKey := func(s *Session) string {
		switch key {
		case "path":
			return s.CWD
		case "name":
			return strings.ToLower(s.Title)
		case "branch":
			if s.Worktree != "" {
				return s.Worktree
			}
			return s.Branch
		}
		return ""
	}
	for _, g := range groups {
		sort.SliceStable(g.Sessions, func(i, j int) bool {
			a, b := g.Sessions[i], g.Sessions[j]
			if key == "activity" {
				return less(b.Modified.Compare(a.Modified))
			}
			if c := strings.Compare(sessionKey(a), sessionKey(b)); c != 0 {
				return less(c)
			}
			return b.Modified.Before(a.Modified)
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if key == "activity" {
			return less(groups[j].Latest.Compare(groups[i].Latest))
		}
		return less(strings.Compare(groups[i].Repo, groups[j].Repo))
	})
}

// maxDepth is how many levels a project can unfold to in the current view.
func (m model) maxDepth() int {
	if m.mrView {
		return 2
	}
	return 1
}

// shown is how many levels of a project are visible.
func (m model) shown(g *Group) int {
	if d, ok := m.depth[g.Repo]; ok {
		return min(d, m.maxDepth())
	}
	return m.maxDepth()
}

// foldMark is the [-] / [+] / [±] marker in front of a project header.
func (m model) foldMark(g *Group) string {
	switch d := m.shown(g); {
	case d == 0:
		return "[+]"
	case d < m.maxDepth():
		return "[±]"
	}
	return "[-]"
}

// toggleFold opens the selected project one level further, or folds it
// when it is fully open. The cursor moves to the project's header, since
// the row it was on may disappear.
func (m *model) toggleFold() {
	g, _ := m.selected()
	if g == nil {
		return
	}
	d := m.shown(g) + 1
	if d > m.maxDepth() {
		d = 0
	}
	m.depth[g.Repo] = d
	for i := m.cursor; i >= 0; i-- {
		if m.rows[i].header() && m.rows[i].group == g {
			m.cursor = i
			break
		}
	}
	m.rebuildRows()
}

// anyOpen reports whether some project shows rows under its header.
func (m model) anyOpen() bool {
	for _, g := range m.pd().groups {
		if m.shown(g) > 0 {
			return true
		}
	}
	return m.shown(unlinkedGroup) > 0 && m.mrView
}

// foldAll folds every project, or opens all of them.
func (m *model) foldAll(open bool) {
	clear(m.depth)
	if !open {
		for _, g := range m.pd().groups {
			m.depth[g.Repo] = 0
		}
		m.depth[unlinkedGroup.Repo] = 0
	}
	m.rebuildRows()
}

// sortLabel is the clickable " sort: <key> ▾" shown in the title bar.
func (m model) sortLabel() string {
	arrow := "▾"
	if m.sortDesc {
		arrow = "▴"
	}
	return " sort: " + sortKeys[m.sortBy] + " " + arrow + " "
}

// rebuildRows flattens groups into the visible list, applying the sort
// order and the filter.
func (m *model) rebuildRows() {
	var sel row
	if m.cursor < len(m.rows) {
		sel = m.rows[m.cursor]
	}
	m.sortGroups(m.pd().groups)
	m.rows = m.rows[:0]
	f := m.filter
	if m.mrView {
		m.buildMRRows(f)
	} else {
		m.buildSessionRows(f)
	}
	// Try to keep the cursor on the same item (or, leaving the MR view, on
	// the MR's session), else on the first session.
	m.cursor = -1
	for _, want := range []row{sel, {group: sel.group, sess: sel.sess}} {
		for i, r := range m.rows {
			if sameRow(r, want) {
				m.cursor = i
				break
			}
		}
		if m.cursor >= 0 || sel.mr == nil {
			break
		}
	}
	if m.cursor < 0 {
		m.cursor = 0
		for i, r := range m.rows {
			if !r.header() {
				m.cursor = i
				break
			}
		}
	}
	m.clampCursor()
}

func sameRow(r, want row) bool {
	switch {
	case want.mr != nil:
		return r.mr != nil && r.mr.URL == want.mr.URL &&
			(r.sess == nil) == (want.sess == nil) && (r.sess == nil || r.sess.ID == want.sess.ID)
	case want.sess != nil:
		return r.mr == nil && r.sess != nil && r.sess.ID == want.sess.ID && r.sess.Profile == want.sess.Profile
	case want.group != nil && want.group.Repo != "":
		return r.header() && r.group.Repo == want.group.Repo
	}
	return false
}

// buildMRRows lists only sessions with open MRs, each followed by its MRs,
// then the open MRs that no visible session claims.
func (m *model) buildMRRows(f string) {
	claimed := map[*MR]bool{}
	for _, g := range m.pd().groups {
		start := len(m.rows)
		m.rows = append(m.rows, row{group: g})
		inGroup := map[*MR]bool{}
		for _, s := range g.Sessions {
			if len(s.MRs) == 0 || s.Archived && !m.showArchived {
				continue
			}
			for _, mr := range s.MRs {
				claimed[mr] = true
			}
			if f != "" && !matches(s, f) && !slices.ContainsFunc(s.MRs, func(mr *MR) bool { return mrMatches(mr, f) }) {
				continue
			}
			if m.shown(g) >= 1 {
				m.rows = append(m.rows, row{group: g, sess: s})
			}
			for _, mr := range s.MRs {
				if m.shown(g) >= 2 {
					m.rows = append(m.rows, row{group: g, sess: s, mr: mr})
				}
				inGroup[mr] = true
			}
		}
		if len(inGroup) == 0 {
			m.rows = m.rows[:start]
			continue
		}
		m.rows[start].n = len(inGroup)
	}
	start := len(m.rows)
	m.rows = append(m.rows, row{group: unlinkedGroup})
	n := 0
	for _, mr := range m.currentMRs() {
		if !claimed[mr] && (f == "" || mrMatches(mr, f)) {
			n++
			if m.shown(unlinkedGroup) > 0 {
				m.rows = append(m.rows, row{group: unlinkedGroup, mr: mr})
			}
		}
	}
	if n == 0 {
		m.rows = m.rows[:start]
	} else {
		m.rows[start].n = n
	}
}

func mrMatches(mr *MR, f string) bool {
	for _, term := range filterTerms(f) {
		if !fuzzyMatch(mr.Title, term) && !containsFold(term, mr.Ref, mr.Project, mr.SourceBranch) {
			return false
		}
	}
	return true
}

// containsFold reports whether one of the texts contains term, ignoring case.
func containsFold(term string, texts ...string) bool {
	term = strings.ToLower(term)
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), term) {
			return true
		}
	}
	return false
}

// matchesText reports whether every term of the filter fuzzy-matches one
// of the texts.
func matchesText(f string, texts ...string) bool {
	for _, term := range filterTerms(f) {
		if !matchesAny(term, texts...) {
			return false
		}
	}
	return true
}

func (m *model) buildSessionRows(f string) {
	// With a filter on, the list is ranked by how well the titles match:
	// sessions by score within a project, projects by their best session.
	type block struct {
		g    *Group
		keep []*Session
		n    int
		best int
	}
	var blocks []block
	for _, g := range m.pd().groups {
		var keep []*Session
		for _, s := range g.Sessions {
			if s.Archived && !m.showArchived {
				continue
			}
			if f == "" || matches(s, f) || m.hits[s.ID] != nil {
				keep = append(keep, s)
			}
		}
		if len(keep) == 0 && (f != "" || !m.showArchived && m.archivedIn(g) > 0) {
			if f == "" || !matchesText(f, shortPath(g.Repo)) {
				continue
			}
		}
		n := len(g.Sessions)
		if !m.showArchived {
			n -= m.archivedIn(g)
		}
		b := block{g: g, keep: keep, n: n}
		if f != "" {
			scores := map[*Session]int{}
			for _, s := range keep {
				scores[s] = filterScore(s, f)
				b.best = max(b.best, scores[s])
			}
			sort.SliceStable(keep, func(i, j int) bool { return scores[keep[i]] > scores[keep[j]] })
		}
		blocks = append(blocks, b)
	}
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].best > blocks[j].best })
	for _, b := range blocks {
		m.rows = append(m.rows, row{group: b.g, n: b.n})
		if m.shown(b.g) == 0 {
			continue
		}
		for _, s := range b.keep {
			m.rows = append(m.rows, row{group: b.g, sess: s})
		}
	}
}

// archivedIn counts the archived sessions of a group.
func (m *model) archivedIn(g *Group) int {
	n := 0
	for _, s := range g.Sessions {
		if s.Archived {
			n++
		}
	}
	return n
}

// liveCounts renders the busy and idle session counts of a profile, for its
// tab in the title bar. Empty when nothing is running there.
func (m model) liveCounts(profile string) string {
	busy, idle := 0, 0
	names := []string{profile}
	if profile == allProfile {
		names = m.realProfiles()
	}
	for _, name := range names {
		for _, ls := range m.live[name] {
			if ls.Status == "busy" {
				busy++
			} else {
				idle++
			}
		}
	}
	var out string
	if busy > 0 {
		out += styleBusy.Render(fmt.Sprintf("●%d", busy)) + " "
	}
	if idle > 0 {
		out += styleIdle.Render(fmt.Sprintf("●%d", idle)) + " "
	}
	return out
}

// archivedTotal counts archived sessions in the current profile.
func (m *model) archivedTotal() int {
	n := 0
	for _, g := range m.pd().groups {
		n += m.archivedIn(g)
	}
	return n
}

// matches reports whether every term of the filter fits the session: fuzzy
// on the title, as a substring of the last prompt, branch, worktree or path,
// or as a prefix of the ID. Fuzzy matching on long
// prompts, paths and IDs would let almost any term through.
func matches(s *Session, f string) bool {
	for _, term := range filterTerms(f) {
		if !fuzzyMatch(s.Title, term) &&
			!containsFold(term, s.LastPrompt, s.Branch, s.Worktree, shortPath(s.Repo), s.Profile) &&
			!strings.HasPrefix(s.ID, term) {
			return false
		}
	}
	return true
}

func (m *model) listHeight() int {
	h := m.height - 4 - m.statusHeight() // title bar, rule, rule, status, keys
	if h < 1 {
		h = 1
	}
	return h
}

// statusHeight is the number of rows the status area takes: one normally,
// more while a text input has wrapped, but always leaving one row for the list.
func (m *model) statusHeight() int {
	n := strings.Count(m.statusView(m.viewWidth()), "\n") + 1
	return max(1, min(n, m.height-5))
}

func (m *model) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func (m *model) clampCursor() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *model) move(delta int) {
	m.cursor += delta
	m.clampCursor()
}

func (m *model) switchProfile(delta int) tea.Cmd {
	n := len(m.profiles)
	m.profile = ((m.profile+delta)%n + n) % n
	m.cursor, m.offset = 0, 0
	m.hits = nil
	m.rebuildRows()
	if !m.pd().loaded {
		return m.reloadCmd()
	}
	return nil
}

// scanCmd searches the files of the tab's sessions for the filter's terms.
func (m *model) scanCmd() tea.Cmd {
	var sessions []*Session
	for _, g := range m.pd().groups {
		sessions = append(sessions, g.Sessions...)
	}
	profile, filter := m.profiles[m.profile], m.filter
	return func() tea.Msg {
		return scanMsg{profile: profile, filter: filter, hits: scanSessions(sessions, filterTerms(filter))}
	}
}

// highlight renders every occurrence of the terms in text in bold,
// ignoring case.
func highlight(text string, terms []string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		n := 0
		for _, t := range terms {
			if i+len(t) <= len(text) && strings.EqualFold(text[i:i+len(t)], t) {
				n = max(n, len(t))
			}
		}
		if n == 0 {
			b.WriteByte(text[i])
			i++
			continue
		}
		b.WriteString(styleKey.Render(text[i : i+n]))
		i += n
	}
	return b.String()
}

func (m model) selected() (*Group, *Session) {
	if m.cursor >= len(m.rows) || len(m.rows) == 0 {
		return nil, nil
	}
	r := m.rows[m.cursor]
	return r.group, r.sess
}

// launch runs argv either inline (pausing the TUI) or in a new terminal.
func (m *model) launch(p Profile, dir string, argv []string, newTerm bool) tea.Cmd {
	if dir == "" {
		m.status = styleErr.Render("nothing selected")
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		m.status = styleErr.Render("directory is gone: " + dir)
		return nil
	}
	if newTerm {
		if err := spawnTerminal(m.cfg, p, dir, argv); err != nil {
			m.status = styleErr.Render(err.Error())
			return nil
		}
		m.status = "spawned: " + strings.Join(argv, " ") + " in " + shortPath(dir)
		return nil
	}
	cmd, tail := inlineCmd(p, dir, argv)
	name := filepath.Base(argv[0])
	cmdline := strings.Join(argv, " ") + " in " + shortPath(dir)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return execDoneMsg{name: name, cmdline: cmdline, stderr: tail.LastLine(), err: err}
	})
}

// where describes where enter (alt=false) or its shifted variant
// (alt=true) opens things.
func (m model) where(alt bool) string {
	if m.cfg.NewTerminalDefault() != alt {
		return "in new term"
	}
	return "here"
}

func (m *model) openClaude(newTerm bool) tea.Cmd {
	g, s := m.selected()
	if s == nil {
		if g == nil || g == unlinkedGroup {
			return nil
		}
		return m.startNew(m.cfg.UseWorktrees())
	}
	if s.Running {
		// Resuming again would run two claudes on one conversation; go to
		// the window it is already running in instead.
		if err := focusWindowOf(s.PID); err != nil {
			m.status = styleErr.Render(fmt.Sprintf("already running (pid %d): %v", s.PID, err))
		} else {
			m.status = "focused " + s.Title
		}
		return nil
	}
	return m.launch(m.profileOf(s), s.CWD, resumeArgv(m.cfg, s), newTerm)
}

func (m *model) openShell(newTerm bool) tea.Cmd {
	g, s := m.selected()
	dir := ""
	if s != nil {
		dir = s.CWD
	} else if g != nil {
		dir = g.Repo
	}
	return m.launch(m.profileOf(s), dir, shellArgv(m.cfg), newTerm)
}

// startNew opens the repo prompt for a new session, prefilled with the
// selected project.
func (m *model) startNew(worktree bool) tea.Cmd {
	repo := ""
	g, s := m.selected()
	if s != nil {
		repo = s.Repo
	} else if g != nil {
		repo = g.Repo
	}
	if repo == "" {
		home, _ := os.UserHomeDir()
		repo = home
	}
	m.launchWT = worktree
	m.input = shortPath(repo)
	m.compl = nil
	m.mode = modeRepo
	return nil
}

// confirmRepo validates the repo prompt and moves on to the name prompt.
func (m *model) confirmRepo() {
	dir := filepath.Clean(expandHome(strings.TrimSpace(m.input)))
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		m.compl = []string{styleErr.Render("not a directory: " + dir)}
		return
	}
	m.status = ""
	if m.launchWT && gitRoot(dir) == "" {
		m.launchWT = false
		m.status = styleDim.Render("not a git repository, starting without a worktree")
	}
	m.launchIn = dir
	m.input = ""
	m.compl = nil
	m.mode = modeName
}

// completePath extends a partially typed directory path, returning the new
// input and the candidates when it is still ambiguous.
func completePath(in string) (string, []string) {
	home, _ := os.UserHomeDir()
	exp := in
	if home != "" && (in == "~" || strings.HasPrefix(in, "~/")) {
		exp = home + in[1:] // keep a trailing slash, unlike filepath.Join
	}
	matches, _ := filepath.Glob(exp + "*")
	var dirs []string
	for _, mt := range matches {
		if st, err := os.Stat(mt); err == nil && st.IsDir() && !strings.HasPrefix(filepath.Base(mt), ".") {
			dirs = append(dirs, mt)
		}
	}
	if len(dirs) == 0 {
		return in, nil
	}
	out := dirs[0]
	if len(dirs) == 1 {
		out += "/"
	} else {
		for _, d := range dirs[1:] {
			for !strings.HasPrefix(d, out) {
				out = out[:len(out)-1]
			}
		}
	}
	if strings.HasPrefix(in, "~") && home != "" && strings.HasPrefix(out, home) {
		out = "~" + out[len(home):]
	}
	var names []string
	for _, d := range dirs {
		names = append(names, filepath.Base(d)+"/")
	}
	if len(dirs) == 1 {
		names = nil
	}
	return out, names
}

// confirmName stores the session name and either launches (plain session)
// or moves on to the branch prompt (worktree session).
func (m *model) confirmName(newTerm bool) tea.Cmd {
	m.launchName = strings.ReplaceAll(strings.TrimSpace(m.input), " ", "-")
	if !m.launchWT {
		m.mode = modeList
		return m.launch(m.current(), m.launchIn, newSessionArgv(m.cfg, m.launchName, false), newTerm)
	}
	m.input = ""
	m.mode = modeBranch
	return nil
}

// confirmBranch launches a worktree session. Without a branch, claude creates
// the worktree itself (branch worktree-<name>); with one, the TUI creates it.
func (m *model) confirmBranch(newTerm bool) tea.Cmd {
	m.mode = modeList
	branch := strings.TrimSpace(m.input)
	name := m.launchName
	if branch == "" {
		return m.launch(m.current(), m.launchIn, newSessionArgv(m.cfg, name, true), newTerm)
	}
	if name == "" {
		name = strings.ReplaceAll(branch, "/", "-")
	}
	path, err := createWorktree(m.launchIn, name, branch)
	if err != nil {
		m.status = styleErr.Render(err.Error())
		return nil
	}
	return m.launch(m.current(), path, newSessionArgv(m.cfg, name, false), newTerm)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampCursor()
		return m, nil

	case tea.KeyboardEnhancementsMsg:
		m.kbOK = true
		return m, nil

	case loadedMsg:
		// While a tab is still filling (the all tab loads profile by
		// profile) the cursor stays at the top instead of following the
		// first session that happened to arrive.
		wasLoaded := m.pd().loaded
		pd := m.data[msg.profile]
		pd.loading = false
		pd.loaded = true
		pd.err = msg.err
		if msg.err == nil {
			pd.groups = msg.groups
			m.archive.apply(pd.groups)
			applyMRs(pd.groups, m.mrs[msg.profile])
		}
		m.mergeAll()
		if msg.profile == m.profiles[m.profile] || m.all() {
			if !wasLoaded {
				m.rows, m.cursor, m.offset = m.rows[:0], 0, 0
			}
			m.rebuildRows()
		}
		return m, nil

	case scanMsg:
		// Results for a filter or tab that changed meanwhile are stale.
		if msg.profile != m.profiles[m.profile] || msg.filter != m.filter {
			return m, nil
		}
		m.scanning = false
		m.hits = msg.hits
		m.status = ""
		if len(msg.hits) == 0 {
			m.status = styleDim.Render("no conversation mentions " + m.filter)
		}
		m.rebuildRows()
		return m, nil

	case rescanMsg:
		// Pick up sessions started outside the TUI or in spawned terminals.
		if m.mode == modeList {
			return m, tea.Batch(m.reloadCmd(), rescanTick())
		}
		return m, rescanTick()

	case forgeTickMsg:
		return m, tea.Batch(m.fetchAllMRsCmd(), forgeTick())

	case mrsMsg:
		m.mrBusy[msg.profile] = false
		m.mrErr[msg.profile] = msg.err
		// On a failure keep the last good set rather than a partial one.
		if msg.err != nil {
			return m, nil
		}
		m.mrs[msg.profile] = msg.mrs
		m.mrCache.Profiles[msg.profile] = msg.mrs
		if err := m.mrCache.save(); err != nil {
			m.status = styleErr.Render("saving MR cache: " + err.Error())
		}
		if pd := m.data[msg.profile]; pd.loaded {
			applyMRs(pd.groups, msg.mrs)
		}
		if msg.profile == m.profiles[m.profile] || m.all() {
			m.rebuildRows()
		}
		return m, nil

	case liveMsg:
		for name, pd := range m.data {
			if name == allProfile {
				continue
			}
			live := loadLive(m.cfg.Profiles[name].ConfigDir)
			m.live[name] = live
			if pd.loaded {
				applyLive(pd.groups, live)
			}
		}
		return m, liveTick()

	case execDoneMsg:
		if msg.err != nil {
			detail := msg.err.Error()
			if msg.stderr != "" {
				detail = msg.stderr
			}
			m.status = styleErr.Render(msg.name + ": " + detail)
			if lipgloss.Width(m.status)+len(msg.cmdline)+4 < m.width {
				m.status += styleDim.Render("  (" + msg.cmdline + ")")
			}
		} else {
			m.status = ""
		}
		return m, m.reloadCmd()

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg, tea.MouseWheelMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	prev := m.lastKey
	m.lastKey = key

	switch m.mode {
	case modeHelp:
		if key == "esc" || key == "q" || key == "?" {
			m.mode = modeList
		}
		return m, nil

	case modeFilter:
		switch key {
		case "esc":
			m.filter, m.input, m.hits = "", "", nil
			m.mode = modeList
			m.rebuildRows()
		case "enter":
			m.mode = modeList
			if m.filter != "" && m.hits == nil && !m.scanning {
				m.scanning = true
				m.status = styleDim.Render("searching conversation texts for " + m.filter + "…")
				return m, m.scanCmd()
			}
		default:
			m.input = editText(m.input, msg)
			m.filter = m.input
			m.hits = nil
			m.rows, m.cursor, m.offset = m.rows[:0], 0, 0 // cursor on the best match
			m.rebuildRows()
		}
		return m, nil

	case modeRepo:
		switch key {
		case "esc":
			m.mode = modeList
			m.compl = nil
		case "enter":
			m.confirmRepo()
		case "tab":
			m.input, m.compl = completePath(m.input)
		default:
			m.input = editText(m.input, msg)
			m.compl = nil
		}
		return m, nil

	case modeName:
		switch key {
		case "esc":
			m.mode = modeList
		case "enter":
			return m, m.confirmName(m.cfg.NewTerminalDefault())
		case "shift+enter":
			return m, m.confirmName(!m.cfg.NewTerminalDefault())
		default:
			m.input = editText(m.input, msg)
		}
		return m, nil

	case modeRename:
		switch key {
		case "esc":
			m.mode = modeList
		case "enter":
			m.mode = modeList
			_, s := m.selected()
			title := strings.TrimSpace(m.input)
			if s == nil || title == "" || title == s.Title {
				return m, nil
			}
			if err := renameSession(s, title); err != nil {
				m.status = styleErr.Render("rename: " + err.Error())
				return m, nil
			}
			s.Title = title
			m.status = "renamed to " + title
		default:
			m.input = editText(m.input, msg)
		}
		return m, nil

	case modeMove:
		switch key {
		case "esc":
			m.mode = modeList
			m.compl = nil
		case "tab":
			_, s := m.selected()
			m.input, m.compl = completeName(m.input, m.moveTargets(s))
		case "enter":
			return m.confirmMove()
		default:
			m.input = editText(m.input, msg)
			m.compl = nil
		}
		return m, nil

	case modeBranch:
		switch key {
		case "esc":
			m.mode = modeList
		case "enter":
			return m, m.confirmBranch(m.cfg.NewTerminalDefault())
		case "shift+enter":
			return m, m.confirmBranch(!m.cfg.NewTerminalDefault())
		default:
			m.input = editText(m.input, msg)
		}
		return m, nil
	}

	// modeList
	m.status = ""
	switch key {
	case "z":
		m.toggleFold()
	case "Z", "shift+z":
		m.foldAll(!m.anyOpen())
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "ctrl+d":
		m.move(m.listHeight() / 2)
	case "ctrl+u":
		m.move(-m.listHeight() / 2)
	case "G", "shift+g", "end":
		m.cursor = len(m.rows) - 1
		m.clampCursor()
	case "g", "home":
		if key == "home" || prev == "g" {
			m.cursor = 0
			m.clampCursor()
			m.lastKey = ""
		}
	case "}", "]":
		m.jumpGroup(1)
	case "{", "[":
		m.jumpGroup(-1)
	case "h", "left", "shift+tab":
		return m, m.switchProfile(-1)
	case "l", "right", "tab":
		return m, m.switchProfile(1)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		i := int(key[0] - '1')
		if i < len(m.profiles) {
			return m, m.switchProfile(i - m.profile)
		}
	case "enter", "o", "shift+enter", "alt+enter", "O", "shift+o":
		if mr := m.selectedMR(); mr != nil {
			m.openMR(mr)
			return m, nil
		}
		alt := key != "enter" && key != "o"
		return m, m.openClaude(m.cfg.NewTerminalDefault() != alt)
	case "m":
		if !m.hasForges() {
			m.status = "no forges configured; add forges to a profile in " + shortPath(m.cfg.path)
			return m, nil
		}
		m.mrView = !m.mrView
		m.rebuildRows()
	case "ctrl+enter", "t":
		return m, m.openShell(m.cfg.NewTerminalDefault())
	case "ctrl+shift+enter", "ctrl+alt+enter", "T", "shift+t":
		return m, m.openShell(!m.cfg.NewTerminalDefault())
	case "a":
		if _, s := m.selected(); s != nil {
			s.Archived = m.archive.toggle(s.ID)
			if err := m.archive.save(); err != nil {
				m.status = styleErr.Render("archive: " + err.Error())
			} else if s.Archived {
				m.status = "archived " + s.Title + styleDim.Render("  (A shows archived)")
			} else {
				m.status = "unarchived " + s.Title
			}
			m.rebuildRows()
		}
	case "A", "shift+a":
		m.showArchived = !m.showArchived
		m.rebuildRows()
	case "s":
		m.setSort((m.sortBy+1)%len(sortKeys), m.sortDesc)
	case "S", "shift+s":
		m.setSort(m.sortBy, !m.sortDesc)
	case "R", "shift+r":
		if _, s := m.selected(); s != nil {
			m.input = s.Title
			m.mode = modeRename
		}
	case "M", "shift+m":
		if _, s := m.selected(); s != nil {
			if len(m.moveTargets(s)) == 0 {
				m.status = "only one profile; add another in " + shortPath(m.cfg.path)
				return m, nil
			}
			if s.Running {
				m.status = styleErr.Render("running; close it before moving")
				return m, nil
			}
			m.input = ""
			m.compl = nil
			m.mode = modeMove
		}
	case "n":
		return m, m.startNew(m.cfg.UseWorktrees())
	case "N", "shift+n":
		return m, m.startNew(!m.cfg.UseWorktrees())
	case "/":
		m.mode = modeFilter
		m.input = m.filter
	case "esc":
		if m.filter != "" {
			m.filter, m.hits = "", nil
			m.rebuildRows()
		}
	case "r":
		fetch := m.fetchMRsCmd(m.profiles[m.profile])
		if m.all() {
			fetch = m.fetchAllMRsCmd()
		}
		return m, tea.Batch(m.reloadCmd(), fetch)
	}
	return m, nil
}

// setSort changes the sort key or direction and says so in the status bar.
func (m *model) setSort(by int, desc bool) {
	m.sortBy, m.sortDesc = by, desc
	m.rows, m.cursor, m.offset = m.rows[:0], 0, 0 // start at the top of the new order
	m.rebuildRows()
	dir := ""
	if desc {
		dir = ", reversed"
	}
	m.status = "sorted by " + sortKeys[by] + dir + styleDim.Render("  (s next key, S reverse)")
}

// handleMouse maps clicks and the wheel onto the keys: a click on the
// title bar switches the tab or the sort order (right-click reverses it),
// a click in the list selects the row, the wheel scrolls.
func (m model) handleMouse(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.mode != modeList {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.move(-3)
		case tea.MouseWheelDown:
			m.move(3)
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft && msg.Button != tea.MouseRight {
			return m, nil
		}
		if msg.Y == 0 {
			_, tabs, sortX := m.titleBar(m.viewWidth())
			if msg.X >= sortX {
				if msg.Button == tea.MouseRight {
					m.setSort(m.sortBy, !m.sortDesc)
				} else {
					m.setSort((m.sortBy+1)%len(sortKeys), m.sortDesc)
				}
				return m, nil
			}
			for i, end := range tabs {
				if msg.X < end {
					return m, m.switchProfile(i - m.profile)
				}
			}
			return m, nil
		}
		if i := m.offset + msg.Y - 2; msg.Y >= 2 && msg.Y < 2+m.listHeight() && i < len(m.rows) {
			m.cursor = i
			m.clampCursor()
			if m.rows[i].header() && msg.X < 5 { // the [-] / [+] marker
				m.toggleFold()
			}
		}
	}
	return m, nil
}

func (m model) selectedMR() *MR {
	if m.cursor >= len(m.rows) || len(m.rows) == 0 {
		return nil
	}
	return m.rows[m.cursor].mr
}

func (m *model) openMR(mr *MR) {
	if err := openURL(mr.URL); err != nil {
		m.status = styleErr.Render("opening " + mr.URL + ": " + err.Error())
		return
	}
	m.status = "opened " + mr.Ref + " " + mr.Title
}

func (m *model) jumpGroup(dir int) {
	i := m.cursor + dir
	for i >= 0 && i < len(m.rows) {
		if m.rows[i].header() {
			m.cursor = i
			m.clampCursor()
			return
		}
		i += dir
	}
}

// editText applies a key press to a single-line text input.
func editText(s string, msg tea.KeyPressMsg) string {
	switch msg.String() {
	case "backspace":
		if len(s) > 0 {
			r := []rune(s)
			return string(r[:len(r)-1])
		}
		return s
	case "ctrl+u":
		return ""
	case "ctrl+w":
		s = strings.TrimRight(s, " ")
		if i := strings.LastIndex(s, " "); i >= 0 {
			return s[:i+1]
		}
		return ""
	}
	if msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper) == 0 {
		return s + msg.Text
	}
	return s
}

// ---------------------------------------------------------------- rendering

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+"/") {
			return "~" + p[len(home):]
		}
	}
	return p
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return t.Format("Jan 02")
	}
}

// truncate cuts a (possibly styled) string to w cells.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

func pad(s string, w int) string {
	n := lipgloss.Width(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

func (m model) View() tea.View {
	var b strings.Builder
	w := m.viewWidth()
	// A wrapped input line shrinks the list; keep the cursor row in view.
	m.clampCursor()

	bar, _, _ := m.titleBar(w)
	b.WriteString(bar + "\n")
	b.WriteString(styleRule.Render(strings.Repeat("─", w)) + "\n")

	switch m.mode {
	case modeHelp:
		b.WriteString(m.helpView())
	default:
		b.WriteString(m.listView(w))
	}

	b.WriteString(styleRule.Render(strings.Repeat("─", w)) + "\n")
	status := strings.Split(m.statusView(w), "\n")
	status = status[len(status)-m.statusHeight():] // keep the end, where the cursor is
	b.WriteString(strings.Join(status, "\n") + "\n")
	b.WriteString(m.keysView(w))

	v := tea.NewView(b.String())
	v.AltScreen = true
	v.WindowTitle = "reserva"
	if m.cfg.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

// titleBar renders the profile tabs, notes and the sort label, cut to w.
// For clicks it also returns the column each tab ends at and the column
// the sort label starts at.
func (m model) titleBar(w int) (bar string, tabEnds []int, sortX int) {
	bar = styleTitle.Render(" reserva ") + " "
	for i, p := range m.profiles {
		label := fmt.Sprintf(" %d:%s ", i+1, p)
		var tab string
		if i == m.profile {
			tab = styleTabOn.Render(label)
		} else {
			tab = styleTab.Render(label) + m.liveCounts(p)
		}
		if m.mrErr[p] != nil {
			tab += styleErr.Render("mr?")
		}
		bar += tab + " "
		tabEnds = append(tabEnds, lipgloss.Width(bar))
	}
	if m.mrView {
		bar += " " + styleHeaderOn.Render(fmt.Sprintf(" %d open MRs ", len(m.currentMRs())))
		if err := m.mrErr[m.profiles[m.profile]]; err != nil {
			bar += " " + styleErr.Render(err.Error())
		}
	}
	if n := m.archivedTotal(); n > 0 {
		hint := fmt.Sprintf("%d archived hidden (A shows)", n)
		if m.showArchived {
			hint = fmt.Sprintf("showing %d archived (A hides)", n)
		}
		bar += " " + styleDim.Render(hint)
	}
	label := m.sortLabel()
	sortX = lipgloss.Width(bar) + 1
	if sortX+len([]rune(label)) > w {
		return truncate(bar, w), tabEnds, w
	}
	return bar + " " + styleTab.Render(label), tabEnds, sortX
}

func (m model) listView(w int) string {
	var b strings.Builder
	h := m.listHeight()
	pd := m.pd()
	if pd.loading && !pd.loaded {
		b.WriteString(styleDim.Render("  loading sessions from " + m.source() + "…"))
		b.WriteString(strings.Repeat("\n", h))
		return b.String()
	}
	if pd.err != nil {
		b.WriteString(styleErr.Render("  " + pd.err.Error()))
		b.WriteString(strings.Repeat("\n", h))
		return b.String()
	}
	if len(m.rows) == 0 {
		msg := "  no sessions in " + m.source()
		switch {
		case m.mrView && !m.all() && len(m.current().Forges) == 0:
			msg = "  no forges configured for this profile (m goes back)"
		case m.mrView && m.filter != "":
			msg = "  no open MRs match " + m.filter
		case m.mrView && m.mrBusy[m.profiles[m.profile]] && m.mrs[m.profiles[m.profile]] == nil:
			msg = "  asking the forges for open MRs…"
		case m.mrView:
			msg = "  no open MRs (m goes back)"
		case m.filter != "":
			msg = "  no sessions match " + m.filter
		}
		b.WriteString(styleDim.Render(msg))
		b.WriteString(strings.Repeat("\n", h))
		return b.String()
	}
	timeW := 7
	branchW := 18
	if w < 70 {
		branchW = 10
	}
	titleW := w - 4 - branchW - timeW - 3
	if titleW < 10 {
		titleW = 10
	}
	profW := 0
	for _, p := range m.realProfiles() {
		profW = max(profW, len(p))
	}
	end := m.offset + h
	if end > len(m.rows) {
		end = len(m.rows)
	}
	for i := m.offset; i < end; i++ {
		r := m.rows[i]
		on := i == m.cursor
		if r.header() {
			label := fmt.Sprintf(" %s %s", m.foldMark(r.group), shortPath(r.group.Repo))
			if r.group == unlinkedGroup {
				label = " " + m.foldMark(r.group) + " open MRs without a session"
			}
			count := fmt.Sprintf(" %d", r.n)
			if r.n == 0 {
				count = " no sessions yet"
			}
			line := pad(truncate(label, w-lipgloss.Width(count)-1)+styleDim.Render(count), w)
			if on {
				line = styleHeaderOn.Render(pad(truncate(label, w-len(count)-1)+count, w))
			} else {
				line = styleHeader.Render(truncate(label, w-len(count)-1)) + styleDim.Render(count)
			}
			b.WriteString(line + "\n")
			continue
		}
		if r.mr != nil {
			b.WriteString(m.mrLine(r, on, w, titleW, branchW, timeW) + "\n")
			continue
		}
		s := r.sess
		mark := " "
		switch {
		case s.Running && s.Status == "busy":
			mark = styleBusy.Render("●")
		case s.Running:
			mark = styleIdle.Render("●")
		case s.Missing:
			mark = styleErr.Render("✗")
		}
		if on {
			mark = lipgloss.NewStyle().Reverse(true).Render(stripStyle(mark))
		}
		branch := s.Branch
		if s.Worktree != "" {
			branch = "⎇ " + s.Worktree
		}
		titleText := s.Title
		if titleText == "" {
			titleText = "(new session, no prompt yet)"
		}
		if s.Archived {
			titleText += "  [archived]"
		}
		// A session that is listed only because its conversation mentions
		// the filter is marked, since its title does not show why.
		if m.hits[s.ID] != nil && !matches(s, m.filter) {
			titleText = "≈ " + titleText
		}
		// In the session list, a session's newest open MR is marked at the
		// end of its title; the MR view lists them under it instead.
		mrMark := ""
		if !m.mrView && len(s.MRs) > 0 {
			mrMark = s.MRs[0].Ref
			if len(s.MRs) > 1 {
				mrMark += fmt.Sprintf("+%d", len(s.MRs)-1)
			}
		}
		// On the all tab, the profile a session belongs to leads its title.
		prof := ""
		if m.all() {
			prof = pad(s.Profile, profW) + " "
		}
		tw := titleW - len(prof)
		if mrMark != "" {
			tw -= len(mrMark) + 1
		}
		title := pad(truncate(titleText, tw), tw)
		titleStyled := styleDim.Render(prof) + styleRow.Render(title)
		title = prof + title
		if mrMark != "" {
			title += " " + mrMark
			titleStyled += " " + styleMR.Render(mrMark)
		}
		branch = pad(truncate(branch, branchW), branchW)
		when := fmt.Sprintf("%*s", timeW, relTime(s.Modified))
		body := fmt.Sprintf("%s  %s %s", title, branch, when)
		if on {
			b.WriteString("  " + mark + " " + styleRowOn.Render(pad(body, w-4)) + "\n")
		} else if s.Missing || s.Archived {
			b.WriteString("  " + mark + " " + styleDim.Render(body) + "\n")
		} else {
			b.WriteString("  " + mark + " " + titleStyled + "  " + styleDim.Render(branch+" "+when) + "\n")
		}
	}
	for i := end - m.offset; i < h; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// mrLine renders an MR row of the MR view, indented under its session, in
// the same columns as sessions: reference and title, merge status, age.
func (m model) mrLine(r row, on bool, w, titleW, branchW, timeW int) string {
	mr := r.mr
	const refW = 6
	textW := max(titleW-2-refW-1, 4)
	text := mr.Title
	if !strings.EqualFold(mr.Project, r.group.Project) {
		text += "  (" + mr.Project + ")"
	}
	ref := pad(mr.Ref, refW)
	text = pad(truncate(text, textW), textW)
	tag, tagStyle := mrTag(mr)
	tag = truncate(tag, branchW)
	when := fmt.Sprintf("%*s", timeW, relTime(mr.Updated))
	if on {
		return "    └ " + styleRowOn.Render(pad(ref+" "+text+"  "+pad(tag, branchW)+" "+when, w-6))
	}
	if tag != "" {
		tag = tagStyle.Render(tag)
	}
	return styleDim.Render("    └ ") + styleMR.Render(ref) + " " + text + "  " + pad(tag, branchW) + " " + styleDim.Render(when)
}

// mrTag is the short merge status shown next to an MR, with its style.
func mrTag(mr *MR) (string, lipgloss.Style) {
	if mr.Draft {
		return "draft", styleDim
	}
	switch mr.Status {
	case "mergeable":
		return mr.Status, styleBusy
	case "conflicts", "needs rebase":
		return mr.Status, styleErr
	}
	return mr.Status, styleIdle
}

func stripStyle(s string) string {
	switch {
	case strings.Contains(s, "●"):
		return "●"
	case strings.Contains(s, "✗"):
		return "✗"
	}
	return " "
}

// inputLine renders a prompt followed by a single-line text input and cursor,
// wrapped onto as many lines of w cells as it needs so all of it stays readable.
func inputLine(prompt, input string, w int) string {
	return ansi.Wrap(prompt+input+"▏", w, "-/")
}

func (m model) statusView(w int) string {
	switch m.mode {
	case modeFilter:
		return inputLine(" /", m.input, w)
	case modeRepo:
		kind := "new session"
		if m.launchWT {
			kind = "new worktree session"
		}
		return inputLine(fmt.Sprintf(" %s — repository: ", kind), m.input, w)
	case modeName:
		kind := "new session"
		if m.launchWT {
			kind = "new worktree session"
		}
		return inputLine(fmt.Sprintf(" %s in %s — name (optional): ", kind, shortPath(m.launchIn)), m.input, w)
	case modeRename:
		return inputLine(" rename session: ", m.input, w)
	case modeMove:
		_, s := m.selected()
		return inputLine(fmt.Sprintf(" move %q from %s to profile: ", s.Title, s.Profile), m.input, w)
	case modeBranch:
		def := "worktree-<generated>"
		if m.launchName != "" {
			def = "worktree-" + m.launchName
		}
		return inputLine(fmt.Sprintf(" worktree %q in %s — branch (empty = %s): ", m.launchName, shortPath(m.launchIn), def), m.input, w)
	}
	if m.status != "" {
		return " " + truncate(m.status, w-2)
	}
	if mr := m.selectedMR(); mr != nil {
		return " " + truncate(mr.URL+"  "+styleDim.Render("⎇ "+mr.SourceBranch), w-2)
	}
	_, s := m.selected()
	if s != nil {
		detail := shortPath(s.CWD)
		if s.LastPrompt != "" && s.LastPrompt != s.Title {
			detail += "  " + styleDim.Render("› "+s.LastPrompt)
		}
		if s.Running {
			detail = fmt.Sprintf("%s (pid %d) ", s.Status, s.PID) + detail
		}
		lines := []string{" " + truncate(detail, w-2)}
		// Under a content search, the lines of the conversation that
		// mention the terms follow, with the terms highlighted.
		for _, snip := range m.hits[s.ID] {
			lines = append(lines, " "+styleDim.Render("≈ ")+highlight(truncate(snip, w-4), filterTerms(m.filter)))
		}
		return strings.Join(lines, "\n")
	}
	if g, _ := m.selected(); g != nil {
		return " " + truncate(shortPath(g.Repo)+"  "+styleDim.Render("enter starts a new session in this repository"), w-2)
	}
	if m.filter != "" {
		return styleDim.Render(" filter: " + m.filter + "  (esc clears)")
	}
	return styleDim.Render(" " + m.source())
}

// source names where the selected tab's sessions come from.
func (m model) source() string {
	if m.all() {
		return "all profiles"
	}
	return shortPath(m.current().ConfigDir)
}

func (m model) keysView(w int) string {
	k := func(key, desc string) string { return styleKey.Render(key) + styleDim.Render(" "+desc) }
	var parts []string
	switch m.mode {
	case modeFilter:
		parts = []string{k("type", "to filter"), k("enter", "search conversation texts too"), k("esc", "clear")}
	case modeRepo:
		if len(m.compl) > 0 {
			return " " + truncate(strings.Join(m.compl, "  "), w-1)
		}
		parts = []string{k("enter", "next"), k("tab", "complete path"), k("esc", "cancel")}
	case modeName:
		if m.launchWT {
			parts = []string{k("enter", "next: branch"), k("esc", "cancel")}
		} else {
			parts = []string{k("enter", "start "+m.where(false)), k("shift+enter", "start "+m.where(true)), k("esc", "cancel")}
		}
	case modeBranch:
		parts = []string{k("enter", "start "+m.where(false)), k("shift+enter", "start "+m.where(true)), k("esc", "cancel")}
	case modeRename:
		parts = []string{k("enter", "rename"), k("ctrl+u", "clear"), k("esc", "cancel")}
	case modeMove:
		if len(m.compl) > 0 {
			return " " + truncate(strings.Join(m.compl, "  "), w-1)
		}
		_, s := m.selected()
		parts = []string{k("enter", "move"), k("tab", strings.Join(m.moveTargets(s), " / ")), k("esc", "cancel")}
	case modeHelp:
		parts = []string{k("esc", "back")}
	default:
		alt := m.where(true)
		switch {
		case m.selectedMR() != nil && m.kbOK:
			parts = []string{k("⏎", "open MR"), k("^⏎", "shell"), k("^⇧⏎", "shell "+alt)}
		case m.selectedMR() != nil:
			parts = []string{k("o", "open MR"), k("t", "shell"), k("T", "shell "+alt)}
		case m.kbOK:
			parts = []string{k("⏎", "claude"), k("⇧⏎", "claude "+alt), k("^⏎", "shell"), k("^⇧⏎", "shell "+alt)}
		default:
			parts = []string{k("o", "claude"), k("O", "claude "+alt), k("t", "shell"), k("T", "shell "+alt)}
		}
		parts = append(parts, k("n", "new"), k("a", "archive"))
		if m.mrView {
			parts = append(parts, k("m", "sessions"))
		} else if m.hasForges() {
			parts = append(parts, k("m", "MRs"))
		}
		parts = append(parts, k("h/l", "profile"), k("/", "filter"), k("?", "help"), k("q", "quit"))
	}
	return " " + truncate(strings.Join(parts, "  "), w-1)
}

func (m model) helpView() string {
	lines := []string{
		"  Navigation   j/k ↑/↓ move   gg/G top/bottom   ctrl+d/u half page   ]/[ next/prev project",
		"               h/l, 1-9, tab switch profile; the all tab lists every profile's sessions",
		"  Folding      z  fold the project under the cursor, or open it one level further",
		"                  ([+] folded, [±] partly open, [-] open)",
		"               Z  fold every project, or open them all when all are folded",
		"",
		"  Profiles     each tab is a Claude config dir (CLAUDE_CONFIG_DIR); a session is resumed",
		"               with the profile it belongs to, and n, N and enter on a header start",
		"               a new session in the current tab's profile (" + m.cfg.DefaultProfile + " on the all tab)",
		"",
		"  Sessions     enter             resume claude code " + pad(m.where(false), 22) + "(or: o)",
		"               shift+enter       resume claude code " + pad(m.where(true), 22) + "(or: O)",
		"               ctrl+enter        shell in the worktree, " + pad(m.where(false), 18) + "(or: t)",
		"               ctrl+shift+enter  shell in the worktree, " + pad(m.where(true), 18) + "(or: T)",
		"               enter on a running session focuses its window (niri, Terminal.app)",
		"               enter on a project header starts a new session there",
		"",
		"  New          n  new session: asks for a repository (tab completes, prefilled with",
		"                  the selected project), a name, and for worktrees a branch",
		"                  (empty = worktree-<name>); N uses the opposite worktree setting",
		"                  in the last prompt: enter starts it " + m.where(false) + ", shift+enter " + m.where(true),
		"",
		"  MRs          m  toggle the MR view: sessions with open MRs on the profile's forges,",
		"                  each MR nested under its session; enter opens the MR in the browser,",
		"                  the other keys act on its session",
		"",
		"  Filter       /  terms (all must match): fuzzy on the title, substring on prompt,",
		"                  branch, worktree and path, prefix of the session ID",
		"               enter in the prompt also searches the conversation texts (prompts,",
		"                  replies, tool output); hits are marked ≈ and previewed under the list",
		"",
		"  Other        a  archive/unarchive    A  show archived    R  rename",
		"               M  move the session to another profile (its file changes config dir)",
		"               s  next sort key (activity, path, name, branch)    S  reverse the order",
		"               r  reload (and refetch MRs)    ?  help    q  quit",
		"",
		"  Marks        ● busy   ● idle   ✗ directory missing   ⎇ worktree   !12 open MR (+n more)",
		"               ●2 next to a profile tab: sessions running there; mr? forge fetch failed",
		"  Config       " + shortPath(m.cfg.path),
	}
	if !m.kbOK {
		lines = append(lines, "",
			"  Note         this terminal did not enable the kitty keyboard protocol, so shift+enter",
			"               and ctrl+enter look like plain enter; use the o / O / t / T fallbacks.")
	}
	h := m.listHeight()
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n") + "\n"
}

// moveTargets lists the profiles a session can move to: every real profile
// but its own.
func (m model) moveTargets(s *Session) []string {
	var out []string
	for _, name := range m.realProfiles() {
		if s == nil || name != s.Profile {
			out = append(out, name)
		}
	}
	return out
}

// completeName extends a partially typed name to the unique candidate that
// starts with it, or returns the candidates that do.
func completeName(in string, names []string) (string, []string) {
	var hits []string
	for _, n := range names {
		if strings.HasPrefix(n, in) {
			hits = append(hits, n)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	return in, hits
}

// confirmMove moves the selected session to the profile named in the
// prompt and reloads both profiles.
func (m model) confirmMove() (tea.Model, tea.Cmd) {
	_, s := m.selected()
	name := strings.TrimSpace(m.input)
	if s == nil || name == "" {
		m.mode = modeList
		m.compl = nil
		return m, nil
	}
	if !slices.Contains(m.moveTargets(s), name) {
		m.compl = []string{styleErr.Render("no such profile: " + name)}
		return m, nil
	}
	m.mode = modeList
	m.compl = nil
	from := s.Profile
	if err := moveSession(s, m.cfg.Profiles[name]); err != nil {
		m.status = styleErr.Render("move: " + err.Error())
		return m, nil
	}
	m.status = fmt.Sprintf("moved %s to %s", s.Title, name)
	return m, m.loadNames(from, name)
}
