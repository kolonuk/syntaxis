// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

// live is a server wired to the real thing: the real local model server from the private test
// environment and real gVisor sandboxes. Nothing is scripted; a run does what the real model
// decides, so the tests ask for simple, checkable things and assert on the real results.
type live struct {
	*env
	model string // "local/<name>"

	mu       sync.Mutex
	notified []string
}

func liveEnv(t *testing.T) *live {
	t.Helper()
	cfg := testenv.Load(t)
	if cfg["OLLAMA_URL"] == "" || cfg["MODEL_SMALL"] == "" {
		testenv.NotTested(t, "OLLAMA_URL and MODEL_SMALL are not in the private test environment")
	}
	e := newEnv(t)
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
	ctx := context.Background()
	pid, err := e.st.UpsertProvider(ctx, store.Provider{Name: "local", Kind: cfg["OLLAMA_KIND"], BaseURL: cfg["OLLAMA_URL"], Concurrency: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := e.st.UpsertModel(ctx, store.Model{ProviderID: pid, Name: cfg["MODEL_SMALL"], ToolsSupported: 1})
	_ = e.st.SetModelEnabled(ctx, mid, true)
	l := &live{env: e, model: "local/" + cfg["MODEL_SMALL"]}
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{Notify: func(_ context.Context, _ int64, title, _ string) {
		l.mu.Lock()
		l.notified = append(l.notified, title)
		l.mu.Unlock()
	}})
	return l
}

// cleanupSandboxes removes every sandbox and workspace volume the test's users made on the real Docker.
func cleanupSandboxes(t *testing.T, e *env) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		users, _ := e.st.ListUsers(ctx)
		for _, u := range users {
			ss, _ := e.st.ListChatSessions(ctx, u.ID)
			for _, s := range ss {
				if s.SandboxID != "" {
					_ = e.app.Sandbox.Destroy(ctx, s.ID, s.SandboxID)
				}
			}
			_ = e.app.Sandbox.RT.RemoveVolume(ctx, sandbox.WorkspaceVolume(u.ID))
		}
	})
}

// send posts a message and waits for the run to end (the real model can take a while).
func (l *live) send(u *client, id, text string) {
	l.t.Helper()
	if code, _, raw := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": text}); code != 202 {
		l.t.Fatalf("send: %d %s", code, raw)
	}
	l.wait(id)
}

func (l *live) wait(id string) {
	l.t.Helper()
	run := l.app.Runs.Get(id)
	if run == nil {
		return
	}
	done := make(chan struct{})
	go func() { run.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Minute):
		run.Stop()
		l.t.Fatal("the run did not finish in 8 minutes")
	}
}

// history returns the stored conversation of a session as one lowercase string.
func (l *live) history(u *client, id string) string {
	_, _, raw := u.do("GET", "/api/sessions/"+id+"/messages", nil)
	return strings.ToLower(string(raw))
}

// sandboxRun runs a command in the session's real sandbox, as the test's own check of what the
// agent did.
func (l *live) sandboxRun(id, script string) string {
	l.t.Helper()
	ctx := context.Background()
	s, err := l.st.GetChatSession(ctx, id)
	if err != nil || s.SandboxID == "" {
		l.t.Fatalf("the session has no sandbox: %v", err)
	}
	r, err := l.app.Sandbox.Exec(ctx, id, s.SandboxID, sandbox.ExecSpec{Cmd: []string{"sh", "-c", script}, Timeout: 30 * time.Second})
	if err != nil {
		l.t.Fatal(err)
	}
	return strings.TrimSpace(r.Stdout)
}

func execSpec(script string) sandbox.ExecSpec {
	return sandbox.ExecSpec{Cmd: []string{"sh", "-c", script}, Timeout: 30 * time.Second}
}
