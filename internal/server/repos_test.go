// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

func TestCheckCloneURL(t *testing.T) {
	const host = "gitlab.example.com"
	for _, c := range []struct {
		url, full string
		ok        bool
	}{
		{"https://gitlab.example.com/gl-user/app.git", "gl-user/app", true},
		{"https://gitlab.example.com/gl-user/app", "gl-user/app", true},
		{"https://GITLAB.example.com/Gl-User/App.git", "gl-user/app", true},
		{"http://gitlab.example.com/gl-user/app.git", "gl-user/app", true},
		{"https://evil.example.net/gl-user/app.git", "gl-user/app", false},
		{"https://gitlab.example.com.evil.net/gl-user/app.git", "gl-user/app", false},
		{"https://user:pw@gitlab.example.com/gl-user/app.git", "gl-user/app", false},
		{"https://gitlab.example.com/gl-user/other.git", "gl-user/app", false},
		{"https://gitlab.example.com/gl-user/app.git?x=1", "gl-user/app", false},
		{"file:///etc/passwd", "gl-user/app", false},
		{"ssh://git@gitlab.example.com/gl-user/app.git", "gl-user/app", false},
		{"", "gl-user/app", false},
	} {
		if err := checkCloneURL(c.url, c.full, host); (err == nil) != c.ok {
			t.Errorf("%q: %v", c.url, err)
		}
	}
	if checkCloneURL("https://x/a/b", "a/b", "") == nil {
		t.Error("an unknown host must not match anything")
	}
}

func TestWorkBranchAndDirNamesAreSafe(t *testing.T) {
	if b := workBranch("abc-DEF_123456789"); b != "syntaxis/abcDEF12" {
		t.Fatalf("%q", b)
	}
	for in, want := range map[string]string{"owner/app": "app", "grp/sub/my.app": "my.app", "o/$(rm)": "rm", "o/..": "repo", "o/.hidden": "repo"} {
		if got := repoDirName(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
}

func TestSessionFromARepositoryClonesItAndOpensAPullRequestWithTheUsersOwnConnection(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fg := &fakeGit{}
	e.app.Connections.Set(fg)
	fake := sandbox.NewFake()
	var calls []sandbox.ExecSpec
	fake.ExecHook = func(c *sandbox.FakeContainer, ex sandbox.ExecSpec) (sandbox.ExecResult, bool) {
		calls = append(calls, ex)
		if ex.Cmd[0] == "git" || strings.Contains(strings.Join(ex.Cmd, " "), "head -c") || strings.Contains(strings.Join(ex.Cmd, " "), "[ -d") {
			return sandbox.ExecResult{ExitCode: 1}, true
		}
		return sandbox.ExecResult{}, false
	}
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	prArgs, _ := json.Marshal(map[string]string{"action": "pr", "title": "Add pagination", "body": "does the thing"})
	sp := &stubProvider{replies: []*provider.ChatResult{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "run_shell", Arguments: json.RawMessage(`{"command":"echo hi"}`)}}},
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "git", Arguments: prArgs}}},
		{Content: "done"},
	}}
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return sp, nil }
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})

	u := e.userSession("a@example.com")
	other := e.userSession("b@example.com")
	if code, _, _ := u.do("PUT", "/api/connections/gitlab", map[string]any{"fields": map[string]string{"GITLAB_TOKEN": "good-token-123"}}); code != 200 {
		t.Fatal("connect")
	}
	repo := map[string]any{"kind": "gitlab", "full_name": "gl-user/app", "clone_url": "https://gitlab.example.com/gl-user/app.git", "default_branch": "main"}
	mk := func(c *client, r map[string]any) (int, map[string]any) {
		code, m, _ := c.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "autonomy": "full", "repo": r})
		return code, m
	}
	// someone who has not connected the service cannot start from it; nor can a wrong address pass
	if code, m := mk(other, repo); code != 400 || !strings.Contains(m["error"].(string), "connect") {
		t.Fatalf("%d %v", code, m)
	}
	bad := map[string]any{"kind": "gitlab", "full_name": "gl-user/app", "clone_url": "https://evil.example.net/gl-user/app.git"}
	if code, _ := mk(u, bad); code != 400 {
		t.Fatalf("a clone address on another host was accepted: %d", code)
	}
	code, s := mk(u, repo)
	if code != 201 || s["repo"] != "gl-user/app" || !strings.HasPrefix(s["repo_branch"].(string), "syntaxis/") || s["title"] != "gl-user/app" {
		t.Fatalf("%d %v", code, s)
	}
	// or a brand-new repository is created first
	if code, m := mk(u, map[string]any{"kind": "gitlab", "create": map[string]any{"name": "fresh", "private": true}}); code != 201 || m["repo"] != "gl-user/fresh" {
		t.Fatalf("%d %v", code, m)
	}

	id := s["id"].(string)
	if code, _, raw := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "add pagination"}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, got, _ := u.do("GET", "/api/sessions/"+id, nil); got["state"] != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	var clone, shell *sandbox.ExecSpec
	for i := range calls {
		j := strings.Join(calls[i].Cmd, " ")
		if strings.Contains(j, "git clone") {
			clone = &calls[i]
		}
		if len(calls[i].Cmd) > 2 && calls[i].Cmd[2] == "echo hi" {
			shell = &calls[i]
		}
	}
	if clone == nil || clone.WorkingDir != "/workspace" || clone.Cmd[len(clone.Cmd)-4] != "/workspace/app" || clone.Cmd[len(clone.Cmd)-3] != "https://gitlab.example.com/gl-user/app.git" {
		t.Fatalf("the repository was not cloned as expected: %+v", clone)
	}
	if shell == nil || shell.WorkingDir != "/workspace/app" {
		t.Fatalf("commands must start in the clone: %+v", shell)
	}
	envs := strings.Join(shell.Env, " ")
	if !strings.Contains(envs, "GITLAB_TOKEN=good-token-123") || !strings.Contains(envs, "GIT_AUTHOR_NAME=gl-user") || strings.Contains(envs, "a@example.com") {
		t.Fatalf("the user's own token and git identity (not their sign-in email) belong in the environment: %s", envs)
	}
	if fg.prArg.Repo != "gl-user/app" || fg.prArg.Title != "Add pagination" || fg.prArg.Head != s["repo_branch"] || fg.prArg.Base != "main" {
		t.Fatalf("the pull request was opened with %+v", fg.prArg)
	}
	// the token never reached the model
	for _, req := range sp.reqs {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "good-token-123") {
				t.Fatal("token in model input")
			}
		}
	}
	// the system prompt tells the agent where it is
	if sys := sp.reqs[0].Messages[0].Content; !strings.Contains(sys, "/workspace/app") || !strings.Contains(sys, s["repo_branch"].(string)) {
		t.Fatalf("the prompt must name the clone and branch")
	}
}
