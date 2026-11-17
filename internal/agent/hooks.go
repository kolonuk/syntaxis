// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// Hooks run a command in the sandbox before or after a tool call, for example a formatter after every
// edit or a check that refuses writes to a protected path. They come from the user's own settings and
// from the repository (.syntaxis/hooks.json). A hook never asks for approval, so a hook command that
// would need it (or that the policy blocks outright) is not run at all: a repository cannot use a
// hook to do what the agent itself would have to ask about.
//
//	{"hooks":[{"event":"post_tool","tools":["write_file","patch_file","apply_patch"],"command":"gofmt -l ."}]}

// Hook events.
const (
	HookPre  = "pre_tool"  // a non-zero exit refuses the tool call
	HookPost = "post_tool" // the output is added to the tool's result
)

// Hook is one configured command.
type Hook struct {
	Event   string   `json:"event"`
	Tools   []string `json:"tools,omitempty"` // empty = every tool that runs something
	Command string   `json:"command"`
	Timeout int      `json:"timeout_seconds,omitempty"`
	Source  string   `json:"-"` // "user" or "repository"
}

// HookFile is the format of a hooks file and of the user's setting.
type HookFile struct {
	Hooks []Hook `json:"hooks"`
}

// Limits.
const (
	MaxHooks       = 20
	maxHookCommand = 1000
	defaultHookSec = 60
	maxHookSec     = 300
	maxHookOutput  = 2000
)

// hookable are the tools a hook may be attached to.
var hookable = map[string]bool{ToolShell: true, ToolRead: true, ToolWrite: true, ToolPatch: true, ToolApply: true, ToolList: true, ToolSearch: true, ToolGit: true, ToolFetch: true}

// ParseHooks reads and checks a hooks file. The error says what is wrong, for the settings page.
func ParseHooks(raw string, source string) ([]Hook, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var f HookFile
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return nil, fmt.Errorf("the hooks are not valid JSON: %w", err)
	}
	if len(f.Hooks) > MaxHooks {
		return nil, fmt.Errorf("at most %d hooks", MaxHooks)
	}
	for i := range f.Hooks {
		h := &f.Hooks[i]
		h.Source = source
		h.Command = strings.TrimSpace(h.Command)
		switch {
		case h.Event != HookPre && h.Event != HookPost:
			return nil, fmt.Errorf("hook %d: event must be %q or %q", i+1, HookPre, HookPost)
		case h.Command == "" || len(h.Command) > maxHookCommand:
			return nil, fmt.Errorf("hook %d: the command needs 1 to %d characters", i+1, maxHookCommand)
		case h.Timeout < 0 || h.Timeout > maxHookSec:
			return nil, fmt.Errorf("hook %d: timeout_seconds can be up to %d", i+1, maxHookSec)
		}
		for _, t := range h.Tools {
			if !hookable[t] {
				return nil, fmt.Errorf("hook %d: %q is not a tool a hook can be attached to", i+1, t)
			}
		}
	}
	return f.Hooks, nil
}

func (h Hook) matches(event, tool string) bool {
	if h.Event != event || !hookable[tool] {
		return false
	}
	if len(h.Tools) == 0 {
		return true
	}
	for _, t := range h.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// runHooks runs the hooks of one event for a tool call. For pre hooks it returns the first refusal
// (a non-empty string); for post hooks it returns what to add to the tool's result.
func (a *Agent) runHooks(ctx context.Context, event string, call string, args json.RawMessage) string {
	if a.Deps.Tools == nil || len(a.Cfg.Hooks) == 0 || a.Cfg.PlanPhase {
		return ""
	}
	var out []string
	for _, h := range a.Cfg.Hooks {
		if !h.matches(event, call) {
			continue
		}
		// a hook cannot ask the user: anything the policy would stop or ask about is not run
		if a.Deps.Policy != nil {
			if r := a.Deps.Policy.Classify(h.Command); r.Blocked || r.Risky {
				a.audit("agent.hook.skipped", firstLine(h.Command, 120)+": "+r.Reason)
				a.emit(EvNotice, "message", "A hook was not run because it would need approval ("+r.Reason+"): "+firstLine(h.Command, 80))
				continue
			}
		}
		sec := h.Timeout
		if sec <= 0 {
			sec = defaultHookSec
		}
		input := string(args)
		if len(input) > 4000 {
			input = input[:4000]
		}
		a.emit(EvPhase, "phase", "tool", "detail", "hook: "+firstLine(h.Command, 60))
		r, err := a.Deps.Tools.Exec(ctx, sandbox.ExecSpec{
			Cmd:       []string{"sh", "-c", h.Command},
			Env:       []string{"SYNTAXIS_HOOK=" + event, "SYNTAXIS_TOOL=" + call, "SYNTAXIS_TOOL_INPUT=" + input},
			Timeout:   time.Duration(sec) * time.Second,
			MaxOutput: 16 << 10,
		})
		text := strings.TrimSpace(r.Stdout + "\n" + r.Stderr)
		if len(text) > maxHookOutput {
			text = text[:maxHookOutput] + "\n[hook output cut]"
		}
		text = a.redact(text)
		failed := err != nil || r.ExitCode != 0 || r.TimedOut
		a.audit("agent.hook", fmt.Sprintf("%s %s %s: exit=%d", event, call, firstLine(h.Command, 80), r.ExitCode))
		if event == HookPre {
			if failed {
				why := text
				if err != nil {
					why = err.Error()
				} else if r.TimedOut {
					why = "the hook timed out"
				}
				return fmt.Sprintf("refused by a hook (%s): %s", firstLine(h.Command, 80), strings.TrimSpace(why))
			}
			continue
		}
		label := fmt.Sprintf("[hook %s: exit %d]", firstLine(h.Command, 60), r.ExitCode)
		if err != nil {
			label, text = "[hook "+firstLine(h.Command, 60)+" could not run]", err.Error()
		}
		if text != "" {
			label += "\n" + text
		}
		out = append(out, label)
	}
	return strings.Join(out, "\n")
}
