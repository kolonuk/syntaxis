// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/testenv"
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

// Starting a session from a repository, with a real Gitea for the connection and checks: another
// user without the connection cannot, a clone address on another host is refused, and a new
// repository can be created first.
func TestSessionFromARepositoryNeedsTheUsersOwnConnectionAndHost(t *testing.T) {
	gs := testenv.Gitea(t)
	e := newEnv(t)
	allowLoopbackConnections(e) // the test Gitea listens on loopback
	seedModel(e)
	u := e.userSession("a@example.com")
	other := e.userSession("b@example.com")
	if code, _, raw := u.do("PUT", "/api/connections/gitea", map[string]any{"fields": map[string]string{"GITEA_TOKEN": gs.Token, "GITEA_URL": gs.URL}}); code != 200 {
		t.Fatalf("connect: %d %s", code, raw)
	}
	if code, _, raw := u.do("POST", "/api/connections/gitea/repos", map[string]any{"name": "app", "private": true}); code != 200 {
		t.Fatalf("create: %d %s", code, raw)
	}
	repo := map[string]any{"kind": "gitea", "full_name": gs.User + "/app", "clone_url": gs.URL + "/" + gs.User + "/app.git", "default_branch": "main"}
	mk := func(c *client, r map[string]any) (int, map[string]any) {
		code, m, _ := c.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "autonomy": "full", "repo": r})
		return code, m
	}
	if code, m := mk(other, repo); code != 400 || !strings.Contains(m["error"].(string), "connect") {
		t.Fatalf("%d %v", code, m)
	}
	bad := map[string]any{"kind": "gitea", "full_name": gs.User + "/app", "clone_url": "https://evil.example.net/" + gs.User + "/app.git"}
	if code, _ := mk(u, bad); code != 400 {
		t.Fatalf("a clone address on another host was accepted: %d", code)
	}
	code, s := mk(u, repo)
	if code != 201 || s["repo"] != gs.User+"/app" || !strings.HasPrefix(s["repo_branch"].(string), "syntaxis/") || s["title"] != gs.User+"/app" {
		t.Fatalf("%d %v", code, s)
	}
	if code, m := mk(u, map[string]any{"kind": "gitea", "create": map[string]any{"name": "fresh", "private": true}}); code != 201 || m["repo"] != gs.User+"/fresh" {
		t.Fatalf("%d %v", code, m)
	}
}

// The clone itself, in a real gVisor sandbox from a real public repository on GitHub, as the
// user's own connection. The run is started with a model address nothing answers on: the clone
// happens before the first model call, and that is the part checked here.
func TestSessionFromARepositoryClonesItOnItsOwnBranch(t *testing.T) {
	tok := testenv.Env(t, "SYNTAXIS_TEST_GITHUB_TOKEN", "cloning from GitHub")
	e := newEnv(t)
	seedModel(e)
	m := realSandbox(t, e)
	testenv.KeepDNSVolume(t, m.Cfg.Image)
	m.SetDNS([]string{"1.1.1.1"}) // a real resolver; the shared file is put back when the test ends
	// its own network, outside the host firewall's rules for a running Syntaxis
	m.Cfg.Network = "syntaxis-test-clone"
	if err := m.RT.EnsureNetwork(context.Background(), m.Cfg.Network); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", "syntaxis-test-clone").Run() })
	e.app.Sandbox = m
	cleanupSandboxes(t, e)
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	u := e.userSession("a@example.com")
	if code, _, raw := u.do("PUT", "/api/connections/github", map[string]any{"fields": map[string]string{"GITHUB_TOKEN": tok}}); code != 200 {
		t.Fatalf("connect: %d %s", code, raw)
	}
	code, s, raw := u.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "autonomy": "full", "repo": map[string]any{
		"kind": "github", "full_name": "octocat/Hello-World", "clone_url": "https://github.com/octocat/Hello-World.git", "default_branch": "master"}})
	if code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	id := s["id"].(string)
	u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "hello"})
	e.app.Runs.Get(id).Wait()
	sess, _ := e.st.GetChatSession(context.Background(), id)
	if sess.SandboxID == "" {
		t.Fatal("no sandbox was made")
	}
	r, err := e.app.Sandbox.Exec(context.Background(), id, sess.SandboxID, execSpec("cd /workspace/Hello-World && git rev-parse --abbrev-ref HEAD && git config --get remote.origin.url && test -f README"))
	if err != nil || r.ExitCode != 0 {
		_, _, ev := u.do("GET", "/api/sessions/"+id+"/events", nil)
		t.Fatalf("the repository was not cloned: %v %+v\nevents: %.1500s", err, r, ev)
	}
	if got := strings.Fields(r.Stdout); len(got) < 2 || got[0] != s["repo_branch"] || strings.Contains(r.Stdout, tok) {
		t.Fatalf("expected the clone on branch %v without the token in its address: %q", s["repo_branch"], r.Stdout)
	}
}

// Opening a pull request with the user's own connection needs the agent to push a branch to a real
// forge from inside a sandbox, and a real model to ask for it.
func TestAgentOpensAPullRequestWithTheUsersConnection(t *testing.T) {
	testenv.NotTested(t, "pushing a branch from a sandbox and opening a pull request: sandboxes cannot reach a private Gitea (by design) and a public forge would be changed")
}
