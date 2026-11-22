// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Slash commands are saved prompts: typing "/review the login code" sends the command's text with
// "$ARGUMENTS" replaced by what follows. They come from three places: built in, the user's own
// (Settings), and the repository's .syntaxis/commands/<name>.md. The expanded text is what is sent
// and what the chat shows, so the user always sees what the agent was asked.

// Command is a saved prompt.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Template    string `json:"template"`
	Source      string `json:"source,omitempty"` // builtin | user | repository
}

const (
	maxUserCommands   = 50
	maxCommandText    = 4000
	maxRepoCommands   = 50
	commandRepoFolder = ".syntaxis/commands"
)

var commandName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

var builtinCommands = []Command{
	{Name: "review", Description: "Review the changes on this branch", Source: "builtin",
		Template: "Review the changes on the current branch against its base branch (use git diff). Look for bugs, missing tests, unsafe handling of input and anything that does not match the project's conventions. List the problems by importance, each with the file and line and a suggested fix. Do not change any files. $ARGUMENTS"},
	{Name: "test", Description: "Run the tests and fix failures", Source: "builtin",
		Template: "Find how this project's tests are run, run them, and fix any failures. Report what you ran and what you changed. $ARGUMENTS"},
	{Name: "explain", Description: "Explain how some code works", Source: "builtin",
		Template: "Explain how this works, reading the code as needed. Do not change any files. $ARGUMENTS"},
}

func userCommandsKey(uid int64) string { return fmt.Sprintf("commands_user:%d", uid) }
func userHooksKey(uid int64) string    { return fmt.Sprintf("hooks_user:%d", uid) }

func validateCommands(cs []Command) error {
	if len(cs) > maxUserCommands {
		return fmt.Errorf("at most %d commands", maxUserCommands)
	}
	seen := map[string]bool{}
	for i := range cs {
		c := &cs[i]
		c.Name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c.Name), "/"))
		c.Description = oneLine(c.Description)
		c.Source = ""
		switch {
		case !commandName.MatchString(c.Name):
			return fmt.Errorf("%q is not a command name: use lowercase letters, digits and dashes, starting with a letter", c.Name)
		case seen[c.Name]:
			return fmt.Errorf("the command /%s is listed twice", c.Name)
		case strings.TrimSpace(c.Template) == "" || len(c.Template) > maxCommandText:
			return fmt.Errorf("/%s: the text needs 1 to %d characters", c.Name, maxCommandText)
		case len(c.Description) > 200:
			return fmt.Errorf("/%s: the description is too long", c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

func (a *App) userCommands(ctx context.Context, uid int64) []Command {
	var cs []Command
	_ = json.Unmarshal([]byte(a.setting(ctx, userCommandsKey(uid), "")), &cs)
	for i := range cs {
		cs[i].Source = "user"
	}
	return cs
}

// parseCommandFile reads a repository command file: an optional first line "description: ...", then the text.
func parseCommandFile(name, content string) Command {
	c := Command{Name: name, Source: "repository", Template: strings.TrimSpace(content)}
	if first, rest, ok := strings.Cut(c.Template, "\n"); ok && strings.HasPrefix(strings.ToLower(first), "description:") {
		c.Description = oneLine(first[len("description:"):])
		c.Template = strings.TrimSpace(rest)
	}
	if c.Description == "" {
		c.Description = oneLine(firstLineOf(strings.TrimSpace(c.Template), 80))
	}
	return c
}

// repoCommands reads the repository's own commands from the session's sandbox.
func (a *App) repoCommands(ctx context.Context, s *store.ChatSession) []Command {
	if s.SandboxID == "" || a.Sandbox == nil || !a.settingBool(ctx, "repo_skills", true) {
		return nil
	}
	dir := sandbox.WorkspacePath
	if s.RepoName != "" {
		dir += "/" + sessionRepoDir(s)
	}
	dir += "/" + commandRepoFolder
	exec := func(c context.Context, e sandbox.ExecSpec) (sandbox.ExecResult, error) {
		if e.WorkingDir == "" {
			e.WorkingDir = "/"
		}
		return a.Sandbox.Exec(c, s.ID, s.SandboxID, e)
	}
	fs := execFS{exec: exec}
	names, err := fs.ListDir(ctx, dir)
	if err != nil {
		return nil
	}
	var out []Command
	for _, n := range names {
		base := strings.TrimSuffix(n, ".md")
		if base == n || !commandName.MatchString(base) || len(out) >= maxRepoCommands {
			continue
		}
		body, _, err := fs.ReadFile(ctx, path.Join(dir, n), maxCommandText)
		if err != nil || strings.TrimSpace(body) == "" {
			continue
		}
		out = append(out, parseCommandFile(base, body))
	}
	return out
}

// commandsFor lists the commands available in a session. When two sources use a name, the user's own
// wins, then the repository's, then the built-in one.
func (a *App) commandsFor(ctx context.Context, s *store.ChatSession, uid int64) []Command {
	byName := map[string]Command{}
	for _, c := range builtinCommands {
		byName[c.Name] = c
	}
	if s != nil {
		for _, c := range a.repoCommands(ctx, s) {
			byName[c.Name] = c
		}
	}
	for _, c := range a.userCommands(ctx, uid) {
		byName[c.Name] = c
	}
	out := make([]Command, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// expandCommand turns "/name arguments" into the command's text. It returns the text unchanged
// when it is not a command (so a message that merely starts with a path still goes through).
func expandCommand(text string, cmds []Command) (string, *Command) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/") {
		return text, nil
	}
	head, args, _ := strings.Cut(t[1:], " ")
	head, args = strings.TrimSpace(head), strings.TrimSpace(args)
	if i := strings.IndexByte(head, '\n'); i >= 0 {
		args = strings.TrimSpace(head[i:] + " " + args)
		head = head[:i]
	}
	for i := range cmds {
		if cmds[i].Name == strings.ToLower(head) {
			body := cmds[i].Template
			if strings.Contains(body, "$ARGUMENTS") {
				body = strings.ReplaceAll(body, "$ARGUMENTS", args)
			} else if args != "" {
				body += "\n\n" + args
			}
			return strings.TrimSpace(body), &cmds[i]
		}
	}
	return text, nil
}

func (a *App) handleSessionCommands(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	cs := a.commandsFor(r.Context(), s, principal(r).Account.ID)
	for i := range cs {
		cs[i].Template = "" // the list is for choosing; the text is sent with the message
	}
	writeJSON(w, 200, cs)
}

func (a *App) handleMyCommands(w http.ResponseWriter, r *http.Request) {
	cs := a.userCommands(r.Context(), principal(r).Account.ID)
	if cs == nil {
		cs = []Command{}
	}
	writeJSON(w, 200, map[string]any{"commands": cs, "builtin": builtinCommands})
}

func (a *App) handleMyCommandsPut(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Commands []Command `json:"commands"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := validateCommands(b.Commands); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	raw, _ := json.Marshal(b.Commands)
	_ = a.Store.SetSetting(r.Context(), userCommandsKey(principal(r).Account.ID), string(raw))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleMyHooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"hooks": a.setting(r.Context(), userHooksKey(principal(r).Account.ID), "")})
}

func (a *App) handleMyHooksPut(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Hooks string `json:"hooks"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, err := agent.ParseHooks(b.Hooks, "user"); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	uid := principal(r).Account.ID
	_ = a.Store.SetSetting(r.Context(), userHooksKey(uid), b.Hooks)
	_, _ = a.Store.AppendAudit(r.Context(), fmt.Sprintf("user:%d", uid), "hooks.change", "user:"+itoa(uid), "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// repoHooksKey names the repository for hook trust: its address, or the session when the hooks
// file is in a plain workspace.
func repoHooksKey(s *store.ChatSession) string {
	if s.RepoURL != "" {
		return s.RepoURL
	}
	return "session:" + s.ID
}

func hooksTrustKey(uid int64) string { return fmt.Sprintf("hooks_trust:%d", uid) }

const maxTrustedHooks = 200

func hooksTrustEntry(repo, hash string) string { return repo + "@" + hash }

// hooksTrusted tells whether the user has trusted exactly this hooks file for this repository.
func (a *App) hooksTrusted(ctx context.Context, uid int64, repo, hash string) bool {
	var list []string
	_ = json.Unmarshal([]byte(a.setting(ctx, hooksTrustKey(uid), "[]")), &list)
	for _, e := range list {
		if e == hooksTrustEntry(repo, hash) {
			return true
		}
	}
	return false
}

func (a *App) trustHooks(ctx context.Context, uid int64, repo, hash string) {
	var list []string
	_ = json.Unmarshal([]byte(a.setting(ctx, hooksTrustKey(uid), "[]")), &list)
	list = append(list, hooksTrustEntry(repo, hash))
	if len(list) > maxTrustedHooks {
		list = list[len(list)-maxTrustedHooks:]
	}
	b, _ := json.Marshal(list)
	_ = a.Store.SetSetting(ctx, hooksTrustKey(uid), string(b))
}

// RepoHooks is the repository's hooks file as it is now.
type RepoHooks struct {
	Present bool         `json:"present"`
	Hash    string       `json:"hash,omitempty"`
	Hooks   []agent.Hook `json:"hooks,omitempty"`
	Error   string       `json:"error,omitempty"`
	Trusted bool         `json:"trusted"`
}

// readRepoHooks reads .syntaxis/hooks.json from the repository (or the workspace) and says whether
// the user has trusted this exact content.
func (a *App) readRepoHooks(ctx context.Context, uid int64, repoKey, repoDir string, fs execFS) RepoHooks {
	raw, _, err := fs.ReadFile(ctx, path.Join(firstNonEmpty(repoDir, sandbox.WorkspacePath), ".syntaxis/hooks.json"), 32<<10)
	if err != nil {
		return RepoHooks{}
	}
	sum := sha256.Sum256([]byte(raw))
	out := RepoHooks{Present: true, Hash: hex.EncodeToString(sum[:])}
	hs, perr := agent.ParseHooks(raw, "repository")
	if perr != nil {
		out.Error = perr.Error()
		return out
	}
	out.Hooks = hs
	out.Trusted = a.hooksTrusted(ctx, uid, repoKey, out.Hash)
	return out
}

// hooksFor collects the user's hooks and the repository's. The repository's hooks run only when the
// user has trusted this exact file for this repository (the hash changes with any edit, so a changed
// file asks again). A broken file is reported and ignored; it must not stop the session.
func (a *App) hooksFor(ctx context.Context, uid int64, repoKey, repoDir string, fs execFS, warn func(string)) []agent.Hook {
	var hooks []agent.Hook
	if hs, err := agent.ParseHooks(a.setting(ctx, userHooksKey(uid), ""), "user"); err == nil {
		hooks = append(hooks, hs...)
	}
	if !a.settingBool(ctx, "repo_hooks", true) {
		return hooks
	}
	rh := a.readRepoHooks(ctx, uid, repoKey, repoDir, fs)
	switch {
	case !rh.Present:
	case rh.Error != "":
		warn("The repository's .syntaxis/hooks.json was ignored: " + rh.Error)
	case len(rh.Hooks) > 0 && !rh.Trusted:
		warn(fmt.Sprintf("This repository defines %d hook(s) (.syntaxis/hooks.json) that would run commands around tool calls. They are not run until you trust them: open the banner above the message box to read and trust them.", len(rh.Hooks)))
	case len(rh.Hooks) > 0:
		hooks = append(hooks, rh.Hooks...)
	}
	return hooks
}

// handleRepoHooks shows the repository's hooks file and whether it is trusted.
func (a *App) handleRepoHooks(w http.ResponseWriter, r *http.Request) {
	s, u, cid, ok := a.fileSession(w, r, false)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.readRepoHooks(r.Context(), u.ID, repoHooksKey(s), a.repoDirOf(s), a.sessionFS(s, cid)))
}

// handleRepoHooksTrust trusts the file the user was shown. The hash must match what is there now,
// so a file changed after it was shown is not trusted by mistake.
func (a *App) handleRepoHooksTrust(w http.ResponseWriter, r *http.Request) {
	s, u, cid, ok := a.fileSession(w, r, false)
	if !ok {
		return
	}
	var b struct {
		Hash string `json:"hash"`
	}
	if !decode(w, r, &b) {
		return
	}
	rh := a.readRepoHooks(r.Context(), u.ID, repoHooksKey(s), a.repoDirOf(s), a.sessionFS(s, cid))
	if !rh.Present || rh.Error != "" || rh.Hash != b.Hash {
		writeErr(w, http.StatusConflict, "the hooks file has changed; read it again before trusting it")
		return
	}
	a.trustHooks(r.Context(), u.ID, repoHooksKey(s), rh.Hash)
	a.fileAudit(r, "hooks.trust", s.ID, repoHooksKey(s)+" "+rh.Hash[:12])
	rh.Trusted = true
	writeJSON(w, http.StatusOK, rh)
}

func (a *App) repoDirOf(s *store.ChatSession) string {
	if s.RepoName == "" {
		return ""
	}
	return sandbox.WorkspacePath + "/" + sessionRepoDir(s)
}

func (a *App) sessionFS(s *store.ChatSession, cid string) execFS {
	return execFS{exec: func(ctx context.Context, spec sandbox.ExecSpec) (sandbox.ExecResult, error) {
		return a.fileExec(ctx, s, cid, spec)
	}}
}
