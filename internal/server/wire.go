// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/checkpoint"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/queue"
	"github.com/kolonuk/syntaxis/internal/rules"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/scanner"
	"github.com/kolonuk/syntaxis/internal/signing"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Queues holds one fair scheduler per provider.
type Queues struct {
	mu sync.Mutex
	m  map[int64]*queue.Scheduler
}

// NewQueues returns an empty set.
func NewQueues() *Queues { return &Queues{m: map[int64]*queue.Scheduler{}} }

// For returns (creating if needed) the scheduler for a provider and keeps its
// concurrency in sync with the admin setting.
func (q *Queues) For(p *store.Provider) *queue.Scheduler {
	q.mu.Lock()
	defer q.mu.Unlock()
	s, ok := q.m[p.ID]
	if !ok {
		s = queue.New(queue.Options{Concurrency: max1(p.Concurrency), MaxWait: 10 * time.Minute})
		q.m[p.ID] = s
	} else {
		s.SetConcurrency(max1(p.Concurrency))
	}
	return s
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// Snapshots lists per-provider queue state for the admin.
func (q *Queues) Snapshots() map[int64][]queue.Entry {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := map[int64][]queue.Entry{}
	for id, s := range q.m {
		es, _ := s.Snapshot()
		out[id] = es
	}
	return out
}

// Pusher delivers a notification to a user (Web Push / email). Optional.
type Pusher func(ctx context.Context, userID int64, title, body string)

// Wiring carries the extra production dependencies for the agent run.
type Wiring struct {
	Queues     *Queues
	Notify     Pusher
	MaxTurns   int
	Window     func(m store.Model) int // effective real context window for a model
	ToolOutput int
}

// DefaultWindow is used only when nothing could be detected (admin is warned
// by the context.size health check). It is deliberately conservative.
const DefaultWindow = 8192

// RunAgentFunc returns the production RunFunc.
func (a *App) RunAgentFunc(w Wiring) RunFunc {
	if w.Queues == nil {
		w.Queues = NewQueues()
	}
	a.Queues = w.Queues
	if w.Window == nil {
		w.Window = func(m store.Model) int {
			if m.ContextOverride > 0 {
				return m.ContextOverride
			}
			return DefaultWindow
		}
	}
	return func(ctx context.Context, run *Run, s *store.ChatSession, u *store.User, message string) {
		a.runSession(ctx, run, s, u, message, w)
	}
}

func (a *App) setState(ctx context.Context, id, state string, in, out int) {
	s, err := a.Store.GetChatSession(ctx, id)
	if err != nil {
		return
	}
	s.State, s.UpdatedAt = state, a.Now().UTC()
	s.TokensIn += int64(in)
	s.TokensOut += int64(out)
	_ = a.Store.UpdateChatSession(ctx, *s)
}

func (a *App) runSession(ctx context.Context, run *Run, s *store.ChatSession, u *store.User, message string, w Wiring) {
	bg := context.Background() // bookkeeping must survive cancellation of the run
	fail := func(msg string) {
		run.Emit(agent.Event{Kind: agent.EvStatus, Data: map[string]any{"status": agent.StatusError, "reason": msg}})
		a.setState(bg, s.ID, "error", 0, 0)
	}
	if a.Sandbox == nil {
		fail("Sandboxes are not available on this server.")
		return
	}
	prov, err := a.Store.GetProvider(bg, s.ProviderID)
	if err != nil || !prov.Enabled {
		fail("The model provider for this session is not available.")
		return
	}
	pr, err := a.ProviderFor(prov)
	if err != nil {
		fail("Could not connect to the model provider.")
		return
	}
	models, _ := a.Store.ListModels(bg, prov.ID)
	var model store.Model
	for _, m := range models {
		if m.Name == s.Model {
			model = m
		}
	}
	if !model.Enabled {
		fail("This model is no longer enabled.")
		return
	}
	a.setState(bg, s.ID, "running", 0, 0)

	// sandbox: reuse if present, else create. Secrets are injected only here.
	cid, err := a.ensureSandbox(ctx, s, u)
	if err != nil {
		fail(err.Error())
		return
	}
	started := a.Now()
	defer func() { _ = a.Quota.RecordSandboxTime(bg, u.ID, a.Now().Sub(started)) }()

	exec := func(c context.Context, e sandbox.ExecSpec) (sandbox.ExecResult, error) {
		return a.Sandbox.Exec(c, s.ID, cid, e)
	}
	// helpers over exec
	runner := func(c context.Context, argv []string, env []string) (string, error) {
		spec := sandbox.ExecSpec{Cmd: argv, Env: env, Timeout: 2 * time.Minute}
		r, err := exec(c, spec)
		if err != nil {
			return "", err
		}
		if r.ExitCode != 0 {
			return r.Stdout, fmt.Errorf("exit %d: %s", r.ExitCode, strings.TrimSpace(r.Stderr))
		}
		return r.Stdout, nil
	}
	cp := &checkpoint.Manager{Run: runner, Workspace: sandbox.WorkspacePath, MaxCount: 50, MaxBytes: 1 << 30}
	fs := execFS{exec: exec}
	loaded, _ := rules.Load(ctx, fs, sandbox.WorkspacePath, rules.Options{ForeignEnabled: a.settingBool(bg, "foreign_instructions", true), RepoSkills: a.settingBool(bg, "repo_skills", true)})
	red, _ := a.Secrets.Redactor(bg, u.ID)
	policy, _ := agent.NewApprovalPolicy()

	mode := scanner.Mode(a.setting(bg, SettingScanMode, a.Cfg.SecretScanMode))
	override := a.settingBool(bg, SettingUserOverride, a.Cfg.AllowUserOverride)

	toolsOn := model.ToolsSupported != 0
	autonomy := agent.Autonomy(s.Autonomy)
	include := map[string]bool{}
	if loaded != nil {
		for _, f := range loaded.Foreign {
			include[f.Path] = true // the UI lets users deselect; default include (advisory, wrapped)
		}
	}
	system := agent.BuildSystemPrompt(agent.PromptOptions{Autonomy: autonomy, ToolsEnabled: toolsOn, Rules: loaded, IncludeForeign: include, CloudToolsOff: u.CloudToolsDisabled})

	signMode := a.setting(bg, "signing_mode", a.Cfg.SigningMode)
	var signer func(context.Context, string) (string, error)
	if a.Signing != nil && signMode != "off" {
		if _, err := a.Signing.EnsureKey(bg, u.ID); err == nil {
			signer = func(c context.Context, payload string) (string, error) {
				return a.Signing.Sign(c, u.ID, []byte(payload))
			}
		}
	}
	_ = signing.ModeOff

	bridge := a.startMCP(ctx, s.ID, run.Emit)
	if bridge != nil {
		defer bridge.Stop(context.Background())
	}
	ex := &agent.Executor{
		Exec: exec, Rules: loaded, Model: s.Model, SigningMode: signMode, Signer: signer,
		Scan: func(text string, force bool) scanner.Decision { return scanner.Evaluate(text, mode, override, force) },
		ConfirmOverride: func(c context.Context, d scanner.Decision) (bool, error) {
			run.Emit(agent.Event{Kind: agent.EvApprovalRequest, Data: map[string]any{"id": "scan-override", "tool": "git", "summary": "Push anyway despite possible secrets?", "reason": d.Message, "risky": true}})
			return run.Approve(c, "scan-override")
		},
		Audit: func(action, detail string) {
			_, _ = a.Store.AppendAudit(bg, fmt.Sprintf("user:%d", u.ID), action, "session:"+s.ID, detail)
		},
	}

	if bridge != nil {
		ex.MCP = bridge
	}
	first := true
	chat := func(c context.Context, req provider.ChatRequest, onDelta func(string)) (*provider.ChatResult, error) {
		done, err := a.Quota.BeginModelCall(c, u, int64(agent.TotalTokens(req.Messages)))
		if err != nil {
			return nil, err
		}
		class := queue.Agent
		if first {
			class, first = queue.Interactive, false
		}
		tk, err := w.Queues.For(prov).Enqueue(u.ID, class)
		if err != nil {
			done(0, 0)
			return nil, err
		}
		if ahead, eta := tk.Position(); ahead > 0 {
			run.Emit(agent.Event{Kind: agent.EvNotice, Data: map[string]any{"message": fmt.Sprintf("Queued: %d ahead of you (about %s).", ahead, eta.Round(time.Second))}})
		}
		release, err := tk.Wait(c)
		if err != nil {
			done(0, 0)
			return nil, err
		}
		defer release()
		res, err := pr.Chat(c, req, onDelta)
		if err != nil {
			done(0, 0)
			return nil, err
		}
		in, out := res.Usage.PromptTokens, res.Usage.CompletionTokens
		if !res.Usage.Reported {
			in, out = agent.TotalTokens(req.Messages), agent.EstimateTokens(res.Content)
		}
		done(int64(in), int64(out))
		return res, nil
	}

	// history from the DB (non-abandoned), so sessions resume
	history, _ := a.Store.ListMessages(bg, s.ID, false)
	var msgs []provider.Message
	for _, m := range history {
		msgs = append(msgs, toProviderMessage(m))
	}

	ag := &agent.Agent{
		Cfg: agent.Config{Model: s.Model, Window: w.Window(model), Autonomy: autonomy, System: system, ToolsEnabled: toolsOn, SkillsEnabled: loaded != nil && len(loaded.Skills) > 0,
			MaxTurns: w.MaxTurns, ToolOutputMax: w.ToolOutput, MaxDuration: 2 * time.Hour},
		Deps: agent.Deps{
			Chat: chat, Tools: ex, Policy: policy,
			Approve: func(c context.Context, r agent.ApprovalRequest) (bool, error) { return run.Approve(c, r.CallID) },
			Answer:  func(c context.Context, _ string) (string, error) { return run.Ask(c) },
			Emit:    run.Emit,
			Persist: func(m provider.Message, in, out int) { a.persistMessage(bg, s.ID, m, in, out) },
			Redact:  nilSafeRedact(red),
			Checkpoint: func(c context.Context, label string) error {
				_, err := cp.Snapshot(c, label)
				return err
			},
			Audit: ex.Audit,
			Now:   a.Now,
		},
		Messages: msgs,
	}
	ag.Deps.Summarize = func(c context.Context, prev string, older []provider.Message) (string, error) {
		req := provider.ChatRequest{Model: s.Model, NumCtx: w.Window(model), Messages: append(append([]provider.Message{{Role: "system", Content: agent.SummaryPrompt}}, summarisable(prev, older)...), provider.Message{Role: "user", Content: "Write the summary now."})}
		res, err := chat(c, req, nil)
		if err != nil {
			return "", err
		}
		return res.Content, nil
	}
	_, _ = a.Store.AppendAudit(bg, fmt.Sprintf("user:%d", u.ID), "agent.run", "session:"+s.ID, "autonomy="+s.Autonomy)
	res := ag.Run(ctx, message)

	state := "idle"
	switch res.Status {
	case agent.StatusNeedsInput:
		state = "needs_input"
	case agent.StatusError:
		state = "error"
	}
	a.setState(bg, s.ID, state, res.PromptTokens, res.CompletionTokens)
	if w.Notify != nil && res.Status != agent.StatusStopped {
		title := "Your session finished"
		if res.Status == agent.StatusNeedsInput {
			title = "Your session needs input"
		}
		w.Notify(bg, u.ID, title, s.Title)
	}
}

func summarisable(prev string, older []provider.Message) []provider.Message {
	var out []provider.Message
	if prev != "" {
		out = append(out, provider.Message{Role: "user", Content: "Previous summary:\n" + prev})
	}
	var b strings.Builder
	for _, m := range older {
		c := m.Content
		if len(c) > 1500 {
			c = c[:1500] + "…"
		}
		fmt.Fprintf(&b, "[%s] %s\n", m.Role, c)
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "  call %s %s\n", tc.Name, string(tc.Arguments))
		}
	}
	return append(out, provider.Message{Role: "user", Content: "Earlier conversation to summarise:\n" + b.String()})
}

func nilSafeRedact(r interface{ String(string) string }) func(string) string {
	if r == nil {
		return func(s string) string { return s }
	}
	return r.String
}

func (a *App) persistMessage(ctx context.Context, sessionID string, m provider.Message, in, out int) {
	tc := ""
	if len(m.ToolCalls) > 0 {
		b, _ := json.Marshal(m.ToolCalls)
		tc = string(b)
	}
	_, _ = a.Store.AppendMessage(ctx, store.Message{SessionID: sessionID, Role: m.Role, Content: m.Content, ToolCalls: tc, ToolCallID: m.ToolCallID, TokensIn: in, TokensOut: out, CreatedAt: a.Now().UTC()})
}

func toProviderMessage(m store.Message) provider.Message {
	pm := provider.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
	if m.ToolCalls != "" {
		_ = json.Unmarshal([]byte(m.ToolCalls), &pm.ToolCalls)
	}
	return pm
}

func (a *App) ensureSandbox(ctx context.Context, s *store.ChatSession, u *store.User) (string, error) {
	bg := context.Background()
	if s.SandboxID != "" {
		if err := a.Sandbox.Resume(ctx, s.ID, s.SandboxID); err == nil {
			return s.SandboxID, nil
		} else if !errors.Is(err, sandbox.ErrNotFound) {
			return "", fmt.Errorf("could not resume the sandbox: %w", err)
		}
	}
	if err := a.Quota.CheckSandboxStart(ctx, u, 0); err != nil {
		return "", err
	}
	env, err := a.Secrets.SandboxEnv(ctx, u.ID)
	if err != nil {
		return "", err
	}
	mode := u.EgressMode
	if mode == "" {
		mode = a.Cfg.EgressMode
	}
	id, err := a.Sandbox.Create(ctx, u.ID, s.ID, env, mode == "none")
	if err != nil {
		return "", err
	}
	if cur, err := a.Store.GetChatSession(bg, s.ID); err == nil {
		cur.SandboxID = id
		_ = a.Store.UpdateChatSession(bg, *cur)
	}
	s.SandboxID = id
	return id, nil
}

func (a *App) setting(ctx context.Context, k, def string) string {
	v, err := a.Store.GetSetting(ctx, k, def)
	if err != nil {
		return def
	}
	return v
}

func (a *App) settingBool(ctx context.Context, k string, def bool) bool {
	d := "false"
	if def {
		d = "true"
	}
	return a.setting(ctx, k, d) == "true"
}

// execFS adapts sandbox exec to rules.FS.
type execFS struct {
	exec func(context.Context, sandbox.ExecSpec) (sandbox.ExecResult, error)
}

func (f execFS) ReadFile(ctx context.Context, p string, maxBytes int) (string, bool, error) {
	r, err := f.exec(ctx, sandbox.ExecSpec{Cmd: []string{"sh", "-c", `[ -f "$1" ] && head -c "$2" -- "$1"`, "_", path.Clean(p), fmt.Sprint(maxBytes + 1)}, Timeout: 20 * time.Second})
	if err != nil {
		return "", false, err
	}
	if r.ExitCode != 0 {
		return "", false, rules.ErrNotExist
	}
	if maxBytes > 0 && len(r.Stdout) > maxBytes {
		return r.Stdout[:maxBytes], true, nil
	}
	return r.Stdout, false, nil
}

func (f execFS) ListDir(ctx context.Context, p string) ([]string, error) {
	r, err := f.exec(ctx, sandbox.ExecSpec{Cmd: []string{"sh", "-c", `[ -d "$1" ] && ls -1 -- "$1"`, "_", path.Clean(p)}, Timeout: 20 * time.Second})
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 {
		return nil, rules.ErrNotExist
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}
