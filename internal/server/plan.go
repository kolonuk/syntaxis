// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// handlePlan is the user's decision on a proposed plan: approve it (optionally after editing it) so
// the agent can do the work, or ask for a revision.
func (a *App) handlePlan(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	var b struct {
		Action   string `json:"action"` // approve | revise
		Plan     string `json:"plan"`   // the edited plan, when approving
		Feedback string `json:"feedback"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !s.PlanMode || s.PlanState != "proposed" {
		writeErr(w, http.StatusConflict, "there is no plan waiting for a decision")
		return
	}
	var text string
	switch b.Action {
	case "approve":
		if p := strings.TrimSpace(b.Plan); p != "" {
			if len(p) > 20_000 {
				writeErr(w, http.StatusBadRequest, "the plan is too long")
				return
			}
			s.PlanText = p
		}
		s.PlanState = "approved"
		text = "The plan is approved. Carry it out now."
		if edited := strings.TrimSpace(b.Plan); edited != "" {
			text += " (The user edited the plan; the version in your instructions is the one to follow.)"
		}
	case "revise":
		if strings.TrimSpace(b.Feedback) == "" || len(b.Feedback) > 20_000 {
			writeErr(w, http.StatusBadRequest, "say what to change")
			return
		}
		s.PlanState = ""
		text = "Please revise the plan: " + b.Feedback
	default:
		writeErr(w, http.StatusBadRequest, "approve or revise")
		return
	}
	if err := a.Store.UpdateChatSession(r.Context(), *s); err != nil {
		writeErr(w, 500, "could not save")
		return
	}
	a.startRun(w, r, s, text)
}

// diffScript prints what the session has changed: everything since the repository's base branch
// (commits and uncommitted work), and the untracked files. Arguments: directory, base branch.
const diffScript = `cd "$1" 2>/dev/null || exit 3
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || exit 4
base="origin/$2"
git rev-parse --verify --quiet "$base" >/dev/null || base=HEAD
printf '### branch %s\n' "$(git rev-parse --abbrev-ref HEAD)"
printf '### status\n'; git status --short
printf '### diff\n'; git diff --no-color "$base" -- . ':(exclude)package-lock.json' ':(exclude)*.lock' | head -c 400000`

// handleDiff shows what the session's agent has changed, for review before anything is pushed.
func (a *App) handleDiff(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	if a.Sandbox == nil || s.SandboxID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": "Nothing has run in this session yet."})
		return
	}
	u := a.user(r)
	cid, err := a.ensureSandbox(r.Context(), s, u)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	dir := sandbox.WorkspacePath
	base := s.RepoBase
	if s.RepoName != "" {
		dir += "/" + repoDirName(s.RepoName)
	}
	res, err := a.Sandbox.Exec(r.Context(), s.ID, cid, sandbox.ExecSpec{Cmd: []string{"sh", "-c", diffScript, "_", dir, base}, WorkingDir: "/", Timeout: 60 * time.Second, MaxOutput: 600_000})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not read the changes: "+err.Error())
		return
	}
	switch res.ExitCode {
	case 3, 4:
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": "This workspace is not a git repository, so there is nothing to compare. Start a session from a repository to review changes."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "text": a.redactFor(r.Context(), u.ID, res.Stdout), "branch": s.RepoBranch, "base": s.RepoBase})
}
