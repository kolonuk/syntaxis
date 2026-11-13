// SPDX-License-Identifier: GPL-2.0-or-later

// Package agent implements the built-in coding agent: a server-side loop that
// turns a model into an engineer working inside the user's sandbox.
package agent

import (
	"regexp"
	"strings"
)

// Autonomy levels per session.
type Autonomy string

// Levels.
const (
	AskAll    Autonomy = "ask"       // ask before running anything
	Workspace Autonomy = "workspace" // auto in workspace, ask for risky (default)
	Full      Autonomy = "full"      // fully autonomous within caps
)

// ValidAutonomy reports whether a is a known level.
func ValidAutonomy(a Autonomy) bool { return a == AskAll || a == Workspace || a == Full }

// Risk is the classification of a command.
type Risk struct {
	Risky   bool
	Reason  string
	Blocked bool // never allowed: not even with approval or full autonomy
}

type riskRule struct {
	re     *regexp.Regexp
	reason string
}

// ApprovalPolicy classifies commands. The pattern list is configurable.
type ApprovalPolicy struct {
	rules []riskRule
}

type patternSpec struct{ pattern, reason string }

// DefaultRiskPatterns are the built-in risky-action patterns.
var DefaultRiskPatterns = []patternSpec{
	{`\bgit\s+push\b[^\n;|&]*(--force\b|--force-with-lease\b|\s-f\b)`, "force push rewrites remote history"},
	{`\bgit\s+push\b[^\n;|&]*--delete\b`, "deletes a remote branch"},
	{`\bgit\s+push\b[^\n;|&]*\s:\S+`, "deletes a remote ref"},
	{`\bgit\s+reset\s+--hard\b`, "discards uncommitted work"},
	{`\bgit\s+clean\b[^\n;|&]*-[a-zA-Z]*f`, "deletes untracked files"},
	{`\bgit\s+branch\s+-D\b`, "force-deletes a branch"},
	{`\bgit\s+checkout\s+--\s+\.`, "discards uncommitted work"},
	{`\b(terraform|tofu)\s+(apply|destroy|import|taint|state\s+rm)\b`, "changes real infrastructure"},
	{`\bpulumi\s+(up|destroy)\b`, "changes real infrastructure"},
	{`\bkubectl\s+(delete|apply|replace|scale|drain|cordon|rollout)\b`, "changes a Kubernetes cluster"},
	{`\bhelm\s+(install|upgrade|uninstall|delete|rollback)\b`, "changes a Kubernetes cluster"},
	{`\bgcloud\b[^\n;|&]*\s(delete|destroy|deploy|create|update|set-iam-policy|add-iam-policy-binding)\b`, "changes cloud resources (may cost money)"},
	{`\baws\b[^\n;|&]*\s(delete-\S+|terminate-\S+|remove-\S+|run-instances|create-\S+|put-\S+|deploy\S*|rm|rb|sync\b[^\n;|&]*--delete)`, "changes cloud resources (may cost money)"},
	{`\baz\b[^\n;|&]*\s(delete|create|deploy|up|update|purge)\b`, "changes cloud resources (may cost money)"},
	{`\b(wrangler|cf)\b[^\n;|&]*\s(deploy|publish|delete|rollback|secret\s+put|r2\s+bucket\s+delete|d1\s+delete)\b`, "deploys or deletes on Cloudflare"},
	{`\b(flyctl|fly)\s+(deploy|launch|destroy|scale|apps\s+destroy|machine\s+destroy|volumes?\s+destroy)\b`, "deploys or deletes on Fly.io"},
	{`\bgh\s+(repo\s+(delete|create|edit)|release\s+(create|delete)|secret\s+set|workflow\s+run)\b`, "changes GitHub resources"},
	{`\b(curl|wget)\b[^\n]*\|\s*(sudo\s+)?(ba|z|da)?sh\b`, "pipes a download into a shell"},
	{`\bsudo\b`, "attempts to elevate privileges"},
	{`\b(mkfs\S*|fdisk|parted)\b`, "formats or partitions a disk"},
	{`\bdd\b[^\n;|&]*\bof=/dev/`, "writes to a raw device"},
	{`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`, "fork bomb"},
	{`\bchmod\s+-R\s+[0-7]*7[0-7]*\s+/(\s|$)`, "recursive permission change on /"},
	{`\bnpm\s+publish\b|\bcargo\s+publish\b|\btwine\s+upload\b|\bdocker\s+push\b`, "publishes a package or image"},
	{`\bdocker\s+(system\s+prune|rm|rmi|volume\s+rm)\b`, "deletes Docker resources"},
}

// NewApprovalPolicy builds a policy from the defaults plus extra patterns.
func NewApprovalPolicy(extra ...string) (*ApprovalPolicy, error) {
	p := &ApprovalPolicy{}
	for _, s := range DefaultRiskPatterns {
		re, err := regexp.Compile(`(?i)` + s.pattern)
		if err != nil {
			return nil, err
		}
		p.rules = append(p.rules, riskRule{re, s.reason})
	}
	for _, e := range extra {
		re, err := regexp.Compile(`(?i)` + e)
		if err != nil {
			return nil, err
		}
		p.rules = append(p.rules, riskRule{re, "matches an admin-configured risky pattern"})
	}
	return p, nil
}

var (
	wsPrefix  = "/workspace"
	spaceNorm = regexp.MustCompile(`\s+`)
)

// Classify reports whether a shell command needs approval.
func (p *ApprovalPolicy) Classify(command string) Risk {
	cmd := spaceNorm.ReplaceAllString(strings.TrimSpace(command), " ")
	if why := CheckCommand(cmd); why != "" {
		return Risk{Risky: true, Blocked: true, Reason: "blocked by policy: " + why}
	}
	for _, r := range p.rules {
		if r.re.MatchString(cmd) {
			return Risk{Risky: true, Reason: r.reason}
		}
	}
	if reason, bad := rmOutsideWorkspace(cmd); bad {
		return Risk{Risky: true, Reason: reason}
	}
	return Risk{}
}

// rmOutsideWorkspace flags recursive rm whose targets are not clearly inside /workspace.
func rmOutsideWorkspace(cmd string) (string, bool) {
	for _, seg := range splitSegments(cmd) {
		f := strings.Fields(seg)
		if len(f) == 0 || f[0] != "rm" {
			continue
		}
		recursive := false
		var targets []string
		for _, a := range f[1:] {
			switch {
			case a == "--recursive" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rR")):
				recursive = true
			case strings.HasPrefix(a, "-"):
			default:
				targets = append(targets, a)
			}
		}
		if !recursive {
			continue
		}
		for _, t := range targets {
			t = strings.Trim(t, `"'`)
			if t == "" {
				continue
			}
			if strings.ContainsAny(t, "$`") || strings.HasPrefix(t, "~") {
				return "recursive delete with an unresolvable or home path", true
			}
			if strings.HasPrefix(t, "/") && !(t == wsPrefix || strings.HasPrefix(t, wsPrefix+"/")) {
				return "recursive delete outside the workspace", true
			}
			if t == "/workspace" || t == "/workspace/" || t == "/workspace/*" {
				return "recursive delete of the whole workspace", true
			}
			if strings.Contains(t, "..") {
				return "recursive delete using a parent-directory path", true
			}
			if t == "*" || t == ".*" {
				// glob in cwd: cwd is the workspace root
				return "recursive delete of everything in the current directory", true
			}
		}
	}
	return "", false
}

func splitSegments(cmd string) []string {
	r := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n", "$(", "\n", "`", "\n", ")", "\n")
	var out []string
	for _, s := range strings.Split(r.Replace(cmd), "\n") {
		s = strings.TrimSpace(s)
		// strip a leading sh -c wrapper quote
		s = strings.TrimPrefix(s, "sh -c ")
		s = strings.TrimPrefix(s, "bash -c ")
		s = strings.Trim(s, `"'`)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// NeedsApproval combines autonomy level and risk.
func NeedsApproval(level Autonomy, r Risk, kind string) (bool, string) {
	switch level {
	case AskAll:
		if r.Risky {
			return true, r.Reason
		}
		return true, "autonomy level is \"ask before running anything\""
	case Workspace:
		if r.Risky {
			return true, r.Reason
		}
		return false, ""
	case Full:
		return false, ""
	}
	return true, "unknown autonomy level"
}
