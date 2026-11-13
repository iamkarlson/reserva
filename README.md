# reserva

A small terminal UI for managing [Claude Code](https://claude.com/claude-code)
sessions across several Claude profiles. It lists every conversation Claude
Code has stored, grouped by repository, and lets you jump back into one with a
single key, either resuming Claude or dropping into a shell in that session's
worktree.

In Brazilian football the *reservas* are the substitutes on the bench, waiting
to be sent on. Here they are your Claude sessions.

```
 reserva   1:default   2:work   3:personal
──────────────────────────────────────────────────────────────────────
 ~/src/webapp 3
   ● Fix flaky login test                   fix/login-retry        1d
     Add dark mode toggle                   main                   6d
     Refactor API client                    ⎇ api-client           5d
 ~/src/reserva 1
   ● Claude Code session TUI                main                  now
──────────────────────────────────────────────────────────────────────
 ~/src/webapp  › let's start with the retry logic
 ⏎ claude  ⇧⏎ claude in new term  ^⏎ shell  ^⇧⏎ shell in new term  n new  h/l profile  / filter  ? help  q quit
```

## Features

- Every Claude Code session on your machine, grouped by repository and sorted
  by recent activity, with live busy/idle markers for sessions that are running.
- Multiple Claude profiles (`~/.claude`, `~/.claude-work`, …, and
  [claude-swap](https://github.com/realiti4/claude-swap) accounts) as tabs,
  each launched with its own `CLAUDE_CONFIG_DIR`.
- Resume a session or open a shell in its worktree, in the current terminal or
  a new window.
- Start new sessions in a fresh git worktree, with a branch name of your
  choosing.
- Rename, archive and filter sessions.
- Optionally, the open merge requests on GitLab, GitHub or Gitea that belong
  to each session (see [Merge requests](#merge-requests)).
- Reads Claude's files; the only write is a rename record (see
  [Configuration](#configuration)).

## Requirements

- Linux or macOS (see [macOS notes](#macos-notes)). Windows is not supported.
- [Go](https://go.dev/dl/) 1.26 or newer, to build it.
- The [`claude` CLI](https://docs.claude.com/en/docs/claude-code) on your `PATH`.
- `git`, for worktree sessions.
- Optionally [`glab`](https://gitlab.com/gitlab-org/cli),
  [`gh`](https://cli.github.com) or [`tea`](https://gitea.com/gitea/tea),
  logged in, for merge requests.

## Install

With Go:

```sh
go install github.com/ai-reserva/reserva@latest
```

This puts the binary in `$(go env GOPATH)/bin` (usually `~/go/bin`); make sure
that directory is on your `PATH`.

From a checkout:

```sh
git clone https://github.com/ai-reserva/reserva
cd reserva
go build -o reserva .
mkdir -p ~/.local/bin && mv reserva ~/.local/bin/
```

With Nix:

```sh
nix run github:ai-reserva/reserva
```

`nix develop` in a checkout gives a shell with Go, gopls, golangci-lint and
delve.

For zsh completion of the flags and profile names, put
[`completions/_reserva`](completions/_reserva) in a directory on your `fpath`,
for example:

```sh
mkdir -p ~/.zsh/completions && cp completions/_reserva ~/.zsh/completions/
# in ~/.zshrc, before compinit:
fpath+=~/.zsh/completions
```

Then run `reserva`. On first run it writes a config file with every
Claude profile it finds (see [Configuration](#configuration)).

## Keys

Navigation is vim-style.

| Key | Action |
| --- | --- |
| `j` / `k`, `gg` / `G`, `ctrl+d` / `ctrl+u` | move through the list |
| `]` / `[` | next / previous project |
| `h` / `l`, `1`–`9`, `tab` | switch Claude profile |
| `z` | fold the project under the cursor, or open a folded one a level further |
| `Z` | fold every project, or open them all when all are folded |
| `enter` | resume Claude Code in the selected session, in a new terminal window (or focus its window if it is already running) |
| `shift+enter` | same, in this terminal |
| `ctrl+enter` | open a shell in the session's worktree, in a new terminal window |
| `ctrl+shift+enter` | same, in this terminal |
| `n` | new session: asks for a repository, then a name (creates a git worktree) |
| `N` | same, without a worktree |
| `R` | rename the selected session |
| `a` | archive or unarchive the selected session |
| `A` | show or hide archived sessions |
| `/` | filter sessions by title, prompt, branch or path (see [Search](#search)) |
| `s` / `S` | next sort key (activity, path, name, branch) / reverse the order |
| `m` | toggle the [merge request](#merge-requests) view |
| `r` | reload (and refetch merge requests) |
| `?` | help |
| `q` | quit |

`n` first asks for the repository. The prompt is prefilled with the selected
project, `tab` completes directory names and `~` is expanded, so you can run
reserva from anywhere and point it at any repo. `enter` on a project
header does the same with that project prefilled. Then it asks for a name,
which becomes the worktree directory name and the session's display name, and
for worktree sessions a branch name. In the last prompt `enter` starts the
session in a new terminal window and `shift+enter` starts it in this one.

Opening things in new windows keeps reserva running as a launcher. Set
`new_terminal = false` in the config to swap this, so `enter` runs in this
terminal (pausing reserva until you exit) and the shifted keys open new
windows. When no terminal is configured at all, everything runs in this
terminal.

Terminals that do not speak the
[kitty keyboard protocol](https://sw.kovidgoyal.net/kitty/keyboard-protocol/)
cannot tell `shift+enter` and `ctrl+enter` apart from `enter`. There `o`, `O`,
`t` and `T` do the same four things, and the footer switches to show them.

### Jumping to running sessions

`enter` or `shift+enter` on a session marked as running does not start a
second Claude on the same conversation. Instead reserva focuses the
window that session is running in. When no window is found, the status bar
says so and nothing is started.

On Linux this needs the [niri](https://github.com/YaLTeR/niri) compositor
and a terminal that runs one process per window (Alacritty, kitty, foot, …;
not daemon modes such as `alacritty msg create-window`). The window is
found by walking up from the Claude process to the terminal that owns a
window, so sessions inside tmux or screen cannot be traced.

On macOS this works for sessions running in Terminal.app. The tab is found
by the tty of the Claude process and selected through AppleScript, so
shells and tmux in between do not matter. Other terminals are not
supported.

## Search

`/` filters the list as you type. The filter is split into terms on spaces
and every term has to match: fuzzily, with the
[sahilm/fuzzy](https://github.com/sahilm/fuzzy) matcher, against the
session's title, or as a plain substring of its last prompt, branch, worktree
name or path (fuzzy matching on long prompts and paths would let almost
anything through), or as a prefix of its ID. A term with an upper-case
letter is matched case-sensitively. With a filter on, the list is ranked by
how well the titles match.


## Worktrees

Worktrees live in `<repo>/.claude/worktrees/<name>`, the place Claude Code
uses itself. With the branch prompt left empty, the session runs
`claude --worktree <name>` and Claude creates the worktree on branch
`worktree-<name>`. With a branch given, reserva runs `git worktree add`
itself (creating the branch from `HEAD`, or checking it out if it already
exists) and starts Claude inside it, so the branch you push can be named
independently of the worktree. Sessions in such worktrees are listed under
the main repository with a `⎇ name` marker, and the shell keys open the
worktree directory.

Set `worktrees = false` in the config to start sessions in place by default;
`N` then does the opposite of the default.

Claude Code refuses to create a worktree in a repository whose trust dialog
has not been accepted yet under that profile. Run `claude` once in the repo
(for example with `N`, or `enter` on the header with `worktrees = false`),
accept the dialog, and `n` works from then on. The repository also needs at
least one commit. When a launch fails, the status bar shows Claude's error
and the exact command that was run.

A freshly started session has no prompt yet and is listed as "(new session,
no prompt yet)" while it runs. The list rescans every 10 seconds, so sessions
started in spawned windows appear on their own; `r` forces it.

## Configuration

`~/.config/reserva/config.toml` (or `$XDG_CONFIG_HOME/reserva/`) is
created on first run. Profiles are discovered from `~/.claude` and
`~/.claude-*` directories, and from the accounts of
[claude-swap](https://github.com/realiti4/claude-swap) (`cswap`), which keeps
one config dir per account under
`~/.local/share/claude-swap/sessions/<n>-<email>`. Those are named after the
local part of the email (`georgy` for `georgy@example.com`). Each profile is a
`CLAUDE_CONFIG_DIR`.

```toml
default_profile = "work"
claude_bin = "claude"
claude_args = []            # appended to every claude invocation
terminal = ""               # defaults to $TERMINAL; e.g. "wezterm start --"
new_terminal = true         # enter opens a new window; false: enter runs here
shell = ""                  # defaults to $SHELL
worktrees = true
sort = "activity"           # initial order: activity, path, name or branch
mouse = false               # clicks select rows, tabs and the sort label
all_tab = "first"           # the all tab goes before (first) or after (last) the profiles

[profiles.work]
config_dir = "~/.claude-work"

[profiles.personal]
config_dir = "~/.claude-personal"
```

Profiles appear as tabs in the order they are written. Each profile's sessions
are read from `<config_dir>/projects/*/*.jsonl`; running sessions are detected
from `<config_dir>/sessions/*.json` and marked with a dot (green busy, yellow
idle).

With more than one profile, an `all` tab comes first: every profile's
sessions in one list, each prefixed with its profile name, so `/` filters by
profile too. `all_tab = "last"` puts it after the
profiles' own tabs instead, which keep their config order. Resuming a session
there uses the profile it belongs to; new sessions (`n`, `N`, `enter` on a
header) start in `default_profile`. reserva opens on `default_profile`'s tab;
`--profile all` opens the all tab instead.

The list is sorted by recent activity: the repository with the newest
session first, newest sessions first within it. `s` cycles through the other
orders, `path` (repository path, then session directory), `name` (title) and
`branch`, and `S` reverses the current one; the title bar shows which is on.
`sort` in the config picks the initial one.

Projects fold: `z` on a project or one of its sessions hides the sessions
under the header, marked `[+]`; `z` again opens it one level further, so in
the [MR view](#merge-requests), which nests MRs under sessions, the sessions
come back first (`[±]`) and their MRs on the next press (`[-]`). `Z` folds
every project, or opens them all when everything is folded.

With `mouse = true`, a click selects a row (on a header's `[-]`/`[+]` marker
it folds or opens the project), the wheel scrolls, clicking a profile tab
switches to it and clicking the sort label cycles it (right-click reverses). Terminals then need `shift` held to select text, which is why it
is off by default.

### Merge requests

Give a profile one or more forges and reserva shows the open merge requests
(GitLab) or pull requests (GitHub, Gitea, Forgejo, Codeberg) that belong to
its sessions:

```toml
[profiles.work]
config_dir = "~/.claude-work"
forges = [{ kind = "gitlab", host = "gitlab.com" }]

[profiles.personal]
config_dir = "~/.claude-personal"
forges = [
  { kind = "github", host = "github.com" },
  { kind = "gitea", host = "codeberg.org", scopes = ["author"] },
]
```

| Key | Meaning |
| --- | --- |
| `kind` | `gitlab` (queried with `glab api`), `github` (with `gh api`) or `gitea` (with `tea api`) |
| `host` | the forge's host name |
| `login` | optional: the `tea` login to use (defaults to `host`), or a `GLAB_CONFIG_DIR` / `GH_CONFIG_DIR` for `glab` / `gh` |
| `scopes` | which open MRs to fetch: any of `author`, `assignee`, `reviewer`; default `["author", "assignee"]` |

reserva never handles tokens; it runs the forge's CLI, so log in with
`glab auth login`, `gh auth login` or `tea login add` first. MRs are fetched in the background
at startup, every two minutes and on `r`, and the last result is cached in
`~/.local/state/reserva/mrs.json` so it shows up straight away. If a fetch
fails, the profile's tab gets a red `mr?` and the last good set stays.
Without forges, reserva makes no network requests at all.

An MR belongs to a session when Claude Code recorded a link to it in the
session (it does this when it creates or works on an MR with `glab` or `gh`),
or when the session's branch is the MR's source branch in the project of the
repository's `origin` remote. The newest one is marked after the session's
title, as `!123` (`+n` when there are more).

`m` switches to the MR view: only sessions with open MRs, each followed by
its MRs with their merge status (draft, mergeable, needs approval, conflicts,
…), and at the bottom the open MRs no session claims. `enter` on an MR opens
it in the browser; the shell, rename and archive keys act on its session.
`/` filters by MR title, reference, project and branch too.

### New-window terminal

New windows are started with the terminal named
by `terminal` (or `$TERMINAL`). For a bare program name reserva adds the
flag that introduces the command: `wezterm start --`, `gnome-terminal --`,
`tilix --`, `ptyxis --`, nothing for `foot`, and `-e` for everything else
(alacritty, kitty, ghostty, xterm, konsole, st, …). If your terminal needs
something different, spell out the full prefix, for example
`terminal = "kitty --single-instance"`. On macOS, `terminal = "Terminal.app"`
opens windows in Terminal.app over AppleScript; it is the default when
reserva itself runs in Terminal.app.

### State and writes

Archiving hides a session from the list without touching Claude's files; the
set of archived session IDs is kept in
`~/.local/state/reserva/archived.json` (or `$XDG_STATE_HOME`). The title
bar shows how many are hidden, `A` reveals them dimmed with an `[archived]`
tag, and `a` on one of those brings it back.

Renaming appends a `custom-title` line to the session's file, the same
record Claude Code writes for `/rename` and `--name`, so the new name also
shows up in Claude's own `/resume` picker. Custom titles take precedence over
the AI-generated title in the list. That is the only write reserva makes
to Claude's directories. Its own state (archived sessions, the merge request
cache) lives under `~/.local/state/reserva/`.

### Flags

| Flag | Meaning |
| --- | --- |
| `--profile <name>` | open this profile first |
| `--config <path>` | use another config file |
| `--list` | print the sessions and exit |
| `--help` | print the flags, a short key reference and the config path |

## macOS notes

reserva builds and runs on macOS, and listing, resuming, renaming,
archiving and starting sessions in the current terminal all work. It was
developed on Linux, though, and macOS has not been tested much. Things to know:

- **New windows.** In Terminal.app, new windows work out of the box: the
  session is started through AppleScript in a new Terminal.app window
  (a login shell runs `cd <dir> && exec claude ...`). From any other
  terminal, opening a new window starts a terminal binary with a command to
  run, and `$TERMINAL` is usually unset on macOS. Without one, reserva runs
  everything in the current terminal. Set `terminal = "Terminal.app"` to
  use Terminal.app anyway. iTerm2 cannot be started with a command and is
  not supported for new windows. WezTerm, kitty and Alacritty work if their
  CLI is on your `PATH`, for example:

  ```toml
  terminal = "wezterm start --"
  # or
  terminal = "/Applications/kitty.app/Contents/MacOS/kitty"
  ```

  The `ghostty` CLI on macOS cannot open windows with a command, so Ghostty
  is not supported for new windows either. Jumping to a running session's
  window works for sessions in Terminal.app only (see
  [Jumping to running sessions](#jumping-to-running-sessions)); in other
  terminals `enter` on a running session just reports that it is running.
  Everything that runs in the current terminal works in any terminal.
- **Modifier keys.** Terminal.app does not support the kitty keyboard
  protocol, so use `o` / `O` / `t` / `T` instead of the `shift`/`ctrl` +
  `enter` combinations. In iTerm2 the combinations work once
  *Report keys using CSI u* is enabled in the profile's Keys settings.
- **File locations** follow the Linux (XDG) layout, `~/.config/reserva`
  and `~/.local/state/reserva`, not `~/Library`.

## License

MIT, see [LICENSE](LICENSE).
