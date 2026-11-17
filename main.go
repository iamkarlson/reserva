// reserva is a small TUI for managing Claude Code sessions across
// several Claude configuration profiles, using git worktrees by default.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
)

func main() {
	var (
		configPath  = flag.String("config", "", "path to config.toml (default ~/.config/reserva/config.toml)")
		profile     = flag.String("profile", "", "profile to open first")
		list        = flag.Bool("list", false, "print sessions and exit")
		importCswap = flag.Bool("import-cswap", false, "add the claude-swap (cswap) accounts that are not in the config as profiles")
	)
	flag.Usage = usage
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reserva:", err)
		os.Exit(1)
	}
	// Ask about new claude-swap accounts only when someone can answer.
	var answers io.Reader
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 && !*list {
		answers = os.Stdin
	}
	if cfg, err = importClaudeSwap(cfg, *importCswap, answers, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "reserva:", err)
		os.Exit(1)
	}
	start := cfg.DefaultProfile
	if *profile != "" {
		if _, ok := cfg.Profiles[*profile]; !ok && *profile != allProfile {
			fmt.Fprintf(os.Stderr, "reserva: unknown profile %q\n", *profile)
			os.Exit(1)
		}
		start = *profile
	}

	if *list {
		printSessions(cfg, start)
		return
	}

	p := tea.NewProgram(newModel(cfg, start))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "reserva:", err)
		os.Exit(1)
	}
}

func printSessions(cfg *Config, profile string) {
	names := []string{profile}
	if profile == allProfile {
		names = cfg.ProfileNames()
	}
	var groups []*Group
	for _, name := range names {
		g, err := loadProfile(cfg.Profiles[name])
		if err != nil {
			fmt.Fprintln(os.Stderr, "reserva:", err)
			os.Exit(1)
		}
		groups = append(groups, g...)
	}
	for _, g := range groups {
		fmt.Println(shortPath(g.Repo))
		for _, s := range g.Sessions {
			mark := " "
			if s.Running {
				mark = "●"
			}
			branch := s.Branch
			if s.Worktree != "" {
				branch = "⎇ " + s.Worktree
			}
			fmt.Printf("  %s %-8s %-50s %-20s %s\n", mark, s.ID[:8], truncate(s.Title, 50), truncate(branch, 20), relTime(s.Modified))
		}
	}
}

// usage prints the flags plus a short key reference, so --help is enough to
// get going without the README.
func usage() {
	w := flag.CommandLine.Output()
	fmt.Fprintln(w, "reserva: a terminal UI for Claude Code sessions across several profiles.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: reserva [--profile <name>|all] [--config <path>] [--list] [--import-cswap]")
	fmt.Fprintln(w)
	flag.PrintDefaults()
	fmt.Fprint(w, `
Keys (vim style; ? inside reserva has the full list):
  j/k  move        ]/[  project       h/l 1-9 tab  profile
  z/Z  fold        /    filter        s/S          sort
  f                  find in the conversations, with a preview
  enter/shift+enter  resume in a new terminal or here (see new_terminal)
  ctrl+enter         shell in the session's worktree
  n/N                new session with or without a worktree (see worktrees)
  a/A  archive     R    rename        m   MR view   r  reload   q  quit

Config: `+defaultConfigPath()+`
Profiles are Claude config dirs (CLAUDE_CONFIG_DIR); a session is resumed
with the profile it belongs to. See the README for everything else.
`)
}
