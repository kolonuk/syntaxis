// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/connections"
	"github.com/kolonuk/syntaxis/internal/devcontainer"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Environments are sandbox images an admin has approved (for example one with Rust or Java). A user
// picks one for a session, or a repository's devcontainer.json is matched to one. Nothing else is
// ever run: a repository cannot name an image of its own, and building images is not offered.

var (
	envNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	imageRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,255}$`)
)

func (a *App) templates(ctx context.Context) []devcontainer.Template {
	ts, _ := a.Store.ListImageTemplates(ctx)
	out := make([]devcontainer.Template, len(ts))
	for i, t := range ts {
		out[i] = devcontainer.Template{Name: t.Name, Image: t.Image, Matches: t.Matches, Description: t.Description}
	}
	return out
}

func (a *App) imagePresent(ctx context.Context, image string) bool {
	if a.Sandbox == nil {
		return false
	}
	ok, err := a.Sandbox.RT.ImagePresent(ctx, image)
	return err == nil && ok
}

// handleEnvironments lists the environments a user can pick.
func (a *App) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, t := range a.templates(r.Context()) {
		if a.imagePresent(r.Context(), t.Image) {
			out = append(out, map[string]any{"name": t.Name, "description": t.Description})
		}
	}
	writeJSON(w, 200, out)
}

func (a *App) adminImageTemplates(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, t := range a.templates(r.Context()) {
		out = append(out, map[string]any{"name": t.Name, "image": t.Image, "matches": t.Matches, "description": t.Description, "present": a.imagePresent(r.Context(), t.Image)})
	}
	writeJSON(w, 200, out)
}

func (a *App) adminImageTemplateSet(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Image       string   `json:"image"`
		Matches     []string `json:"matches"`
		Description string   `json:"description"`
	}
	if !decode(w, r, &b) {
		return
	}
	name := chi.URLParam(r, "name")
	if !envNameRe.MatchString(name) {
		writeErr(w, 400, "a name is lowercase letters, digits and dashes")
		return
	}
	if !imageRefRe.MatchString(b.Image) {
		writeErr(w, 400, "that is not an image reference")
		return
	}
	var ms []string
	for _, m := range b.Matches {
		if m = strings.TrimSpace(m); m != "" {
			if !imageRefRe.MatchString(m) {
				writeErr(w, 400, "\""+m+"\" is not an image reference")
				return
			}
			ms = append(ms, m)
		}
	}
	if len(b.Description) > 200 {
		b.Description = b.Description[:200]
	}
	if err := a.Store.UpsertImageTemplate(r.Context(), store.ImageTemplate{Name: name, Image: b.Image, Matches: ms, Description: b.Description}); err != nil {
		writeErr(w, 500, "could not save")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "environment.set", "environment:"+name, b.Image)
	writeJSON(w, 200, map[string]any{"present": a.imagePresent(r.Context(), b.Image)})
}

func (a *App) adminImageTemplateDelete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := a.Store.DeleteImageTemplate(r.Context(), name); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "environment.delete", "environment:"+name, "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// chooseEnvironment sets the image a new session runs in: the one the user picked, or what the
// repository's devcontainer.json asks for if that matches an approved environment. Anything else is
// the default environment, with a note saying why.
func (a *App) chooseEnvironment(ctx context.Context, s *store.ChatSession, creds connections.Creds, git connections.GitProvider, template string) error {
	ts := a.templates(ctx)
	pick := func(t devcontainer.Template, why string) {
		if !a.imagePresent(ctx, t.Image) {
			s.EnvNote = "The " + t.Name + " environment is not built on this server, so the default one is used."
			return
		}
		s.Image, s.EnvNote = t.Image, why
	}
	if template != "" {
		for _, t := range ts {
			if t.Name == template {
				pick(t, "You chose the "+t.Name+" environment.")
				return nil
			}
		}
		return errors.New("that environment is not available")
	}
	if git == nil || s.RepoName == "" || len(ts) == 0 {
		return nil
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, p := range devcontainer.Paths {
		b, err := git.ReadFile(c, creds, s.RepoName, s.RepoBase, p)
		if err != nil {
			continue // no such file, or the service could not say: the default environment
		}
		cfg, err := devcontainer.Parse(b)
		if err != nil {
			s.EnvNote = "The repository's devcontainer.json could not be read, so the default environment is used."
			return nil
		}
		if t, why, ok := devcontainer.Choose(cfg, ts); ok {
			pick(t, why+".")
		} else if why != "" {
			s.EnvNote = why + ". The default environment is used."
		}
		return nil
	}
	return nil
}

// readDevcontainer reads the file from the clone inside the sandbox.
func readDevcontainer(ctx context.Context, exec func(context.Context, sandbox.ExecSpec) (sandbox.ExecResult, error), repoDir string) *devcontainer.Config {
	for _, p := range devcontainer.Paths {
		r, err := exec(ctx, sandbox.ExecSpec{WorkingDir: repoDir, Timeout: 20 * time.Second, Cmd: []string{"sh", "-c", `[ -f "$1" ] && head -c 262144 -- "$1"`, "_", p}})
		if err != nil || r.ExitCode != 0 {
			continue
		}
		if c, err := devcontainer.Parse([]byte(r.Stdout)); err == nil {
			return c
		}
	}
	return nil
}

// applyDevcontainer uses what the repository's devcontainer.json asks for beyond the image: its
// plain environment variables, and its setup command (for example npm install), run once. With
// autonomy "ask" nothing from a repository runs unasked: the user is shown the command instead.
func (a *App) applyDevcontainer(ctx context.Context, run *Run, s *store.ChatSession, exec func(context.Context, sandbox.ExecSpec) (sandbox.ExecResult, error), repoDir string, env *map[string]string) {
	notice := func(m string) {
		run.Emit(agent.Event{Kind: agent.EvNotice, Data: map[string]any{"message": m}})
	}
	cfg := readDevcontainer(ctx, exec, repoDir)
	if cfg == nil {
		return
	}
	ok := map[string]string{}
	for k, v := range cfg.ContainerEnv {
		if !deniedEnv(k) {
			ok[k] = v
		}
	}
	*env = ok
	if cfg.PostCreate == "" {
		return
	}
	marker := sandbox.WorkspacePath + "/.syntaxis-setup-" + s.ID
	if r, err := exec(ctx, sandbox.ExecSpec{Timeout: 20 * time.Second, Cmd: []string{"test", "-e", marker}}); err == nil && r.ExitCode == 0 {
		return // already done in an earlier turn
	}
	if agent.Autonomy(s.Autonomy) == agent.AskAll {
		notice("The repository has a setup command (" + cfg.PostCreate + "). It is not run automatically because you asked to approve everything; ask the agent to run it if you want it.")
		return
	}
	notice("Running the repository's setup command: " + cfg.PostCreate)
	r, err := exec(ctx, sandbox.ExecSpec{Timeout: 10 * time.Minute, Cmd: []string{"sh", "-c", cfg.PostCreate}})
	switch {
	case err != nil:
		notice("The setup command could not run: " + err.Error())
	case r.ExitCode != 0:
		notice("The setup command failed (exit " + strconv.Itoa(r.ExitCode) + "); the agent can look at it. " + clipStr(strings.TrimSpace(r.Stderr+r.Stdout), 400))
	default:
		_, _ = exec(ctx, sandbox.ExecSpec{Timeout: 20 * time.Second, Cmd: []string{"touch", marker}})
		notice("The setup command finished.")
	}
}

func clipStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// deniedEnv are variables a repository's containerEnv may not set: they change how every command runs.
func deniedEnv(k string) bool {
	switch k {
	case "PATH", "HOME", "USER", "SHELL", "IFS", "PS1", "PWD", "TERM", "TMPDIR", "BASH_ENV", "ENV":
		return true
	}
	return strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "SYNTAXIS_")
}
