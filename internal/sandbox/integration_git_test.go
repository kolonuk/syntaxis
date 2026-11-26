// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/connections"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

// TestIntegrationGitPushFromASandbox is the agent's pull-request flow with a real account and a real
// sandbox: clone a repository, change a file, commit, push a branch with the token the sandbox gets
// from the user's connection, and open a pull request. It creates a private repository named
// syntaxis-test-<random> and deletes it at the end, so it runs only with
// SYNTAXIS_TEST_GITHUB_WRITE=1 and a token that may create and delete repositories. The token lives
// only in the test's environment.
func TestIntegrationGitPushFromASandbox(t *testing.T) {
	tok := testenv.Env(t, "SYNTAXIS_TEST_GITHUB_TOKEN", "the GitHub connection")
	if os.Getenv("SYNTAXIS_TEST_GITHUB_WRITE") != "1" {
		testenv.NotTested(t, "set SYNTAXIS_TEST_GITHUB_WRITE=1 to create and delete a throwaway GitHub repository")
	}
	m, d := integrationManager(t)
	ctx := context.Background()
	// a network of its own with a public resolver, as the DNS test does, so a running instance's
	// network and resolver are left alone
	testenv.KeepDNSVolume(t, m.Cfg.Image)
	m.Cfg.Network = "syntaxis-git-it"
	m.SetDNS([]string{"1.1.1.1"})
	if err := d.EnsureNetwork(ctx, m.Cfg.Network); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", "syntaxis-git-it").Run() })

	gp, ok := connections.New(http.DefaultClient).Git("github")
	if !ok {
		t.Fatal("no github provider")
	}
	creds := connections.Creds{"GITHUB_TOKEN": tok}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	repo, err := gp.CreateRepo(ctx, creds, connections.NewRepo{Name: "syntaxis-test-" + hex.EncodeToString(suffix), Description: "throwaway, made by a test", Private: true})
	if err != nil {
		t.Fatalf("create the repository: %v", err)
	}
	t.Cleanup(func() {
		req, _ := http.NewRequest("DELETE", "https://api.github.com/repos/"+repo.FullName, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusNoContent {
			t.Errorf("DELETE THE TEST REPOSITORY %s BY HAND: %v", repo.FullName, err)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
	})

	const uid = 910240
	id, err := m.Create(ctx, uid, "it-gitpush", map[string]string{"GITHUB_TOKEN": tok}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = m.Destroy(ctx, "it-gitpush", id)
		_ = d.RemoveVolume(ctx, WorkspaceVolume(uid))
	})
	sh := func(script string) string {
		r, err := m.Exec(ctx, "it-gitpush", id, ExecSpec{Cmd: []string{"sh", "-c", script}, Timeout: 90 * time.Second})
		if err != nil {
			t.Fatalf("%s: %v", script, err)
		}
		if r.ExitCode != 0 {
			t.Fatalf("%s: exit %d\n%s%s", script, r.ExitCode, r.Stdout, r.Stderr)
		}
		return r.Stdout
	}
	// the clone and the push authenticate through the image's credential helper, from the token in the environment
	sh("cd /workspace && git clone -q " + repo.CloneURL + " work")
	sh("cd /workspace/work && git config user.email test@example.com && git config user.name test && git checkout -q -b syntaxis-change && echo 'hello from a sandbox' > change.txt && git add change.txt && git commit -q -m 'a small change'")
	sh("cd /workspace/work && git push -q -u origin syntaxis-change")
	if out := sh("cd /workspace/work && git ls-remote --heads origin syntaxis-change"); !strings.Contains(out, "refs/heads/syntaxis-change") {
		t.Fatalf("the branch is not on the remote: %q", out)
	}
	// the token is not left in the repository's configuration or in the remote URL
	if out := sh("cd /workspace/work && git remote -v && cat .git/config"); strings.Contains(out, tok) {
		t.Fatal("the token is stored in the repository")
	}
	url, err := gp.OpenPR(ctx, creds, connections.PullRequest{Repo: repo.FullName, Title: "A small change", Body: "opened by a test", Head: "syntaxis-change"})
	if err != nil || !strings.Contains(url, "/pull/") {
		t.Fatalf("open the pull request: %q %v", url, err)
	}
}
