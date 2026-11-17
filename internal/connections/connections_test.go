// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/testenv"
)

var ctx = context.Background()

// svc is the real provider for a kind, built the way the server builds it.
func svc(t *testing.T, kind string) Provider {
	t.Helper()
	p, ok := New(nil).Get(kind)
	if !ok {
		t.Fatalf("no provider %s", kind)
	}
	return p
}

func git(t *testing.T, kind string) GitProvider {
	t.Helper()
	g, ok := New(nil).Git(kind)
	if !ok {
		t.Fatalf("no git provider %s", kind)
	}
	return g
}

// These tests talk to the real services. Where a service needs an account that is not available
// the test skips and says NOT TESTED, naming the variable that would enable it. Nothing here
// pretends to be a service.

func TestGitHubRejectsAWrongTokenWithoutRepeatingIt(t *testing.T) {
	testenv.Network(t, "api.github.com:443")
	g := git(t, "github")
	_, err := g.Test(ctx, Creds{"GITHUB_TOKEN": "ghp_supersecretvalue0000000000000000000000"})
	if !errors.Is(err, ErrBadCredentials) || strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("%v", err)
	}
}

func TestGitHubWithARealToken(t *testing.T) {
	tok := testenv.Env(t, "SYNTAXIS_TEST_GITHUB_TOKEN", "the GitHub connection")
	g := git(t, "github")
	c := Creds{"GITHUB_TOKEN": tok}
	id, err := g.Test(ctx, c)
	if err != nil || id.Account == "" {
		t.Fatalf("%+v %v", id, err)
	}
	rs, err := g.ListRepos(ctx, c, "")
	if err != nil || len(rs) == 0 || !strings.HasPrefix(rs[0].CloneURL, "https://github.com/") {
		t.Fatalf("%+v %v", rs, err)
	}
	// a real devcontainer.json from a public repository, and a real missing file
	if b, err := g.ReadFile(ctx, c, "microsoft/vscode-remote-try-node", "", ".devcontainer/devcontainer.json"); err != nil || !strings.Contains(string(b), "postCreateCommand") {
		t.Fatalf("%v %.80s", err, b)
	}
	if _, err := g.ReadFile(ctx, c, "octocat/Hello-World", "", ".devcontainer/devcontainer.json"); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("%v", err)
	}
	// creating repositories and opening pull requests change a real account, so they are not run
	t.Log("NOT TESTED: creating a GitHub repository and opening a pull request (they would change the real account)")
}

func TestGitLabRejectsAWrongToken(t *testing.T) {
	testenv.Network(t, "gitlab.com:443")
	_, err := svc(t, "gitlab").Test(ctx, Creds{"GITLAB_TOKEN": "glpat-0000000000000000000x"})
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
}

func TestGitLabWithARealToken(t *testing.T) {
	tok := testenv.Env(t, "SYNTAXIS_TEST_GITLAB_TOKEN", "the GitLab connection")
	g := git(t, "gitlab")
	c := Creds{"GITLAB_TOKEN": tok}
	if id, err := g.Test(ctx, c); err != nil || id.Account == "" {
		t.Fatalf("%+v %v", id, err)
	}
	if _, err := g.ListRepos(ctx, c, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCloudflareRejectsAWrongToken(t *testing.T) {
	testenv.Network(t, "api.cloudflare.com:443")
	_, err := svc(t, "cloudflare").Test(ctx, Creds{"CLOUDFLARE_API_TOKEN": "0000000000000000000000000000000000000000"})
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
}

func TestCloudflareWithARealToken(t *testing.T) {
	tok := testenv.Env(t, "SYNTAXIS_TEST_CLOUDFLARE_TOKEN", "the Cloudflare connection")
	if id, err := svc(t, "cloudflare").Test(ctx, Creds{"CLOUDFLARE_API_TOKEN": tok}); err != nil || id.Account == "" {
		t.Fatalf("%+v %v", id, err)
	}
}

func TestIFTTTRejectsAWrongKey(t *testing.T) {
	testenv.Network(t, "maker.ifttt.com:443")
	_, err := svc(t, "ifttt").Test(ctx, Creds{"IFTTT_WEBHOOK_KEY": "nope"})
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
}

func TestIFTTTWithARealKey(t *testing.T) {
	key := testenv.Env(t, "SYNTAXIS_TEST_IFTTT_KEY", "the IFTTT connection")
	if _, err := svc(t, "ifttt").Test(ctx, Creds{"IFTTT_WEBHOOK_KEY": key}); err != nil {
		t.Fatal(err)
	}
}

func TestAWSRejectsWrongKeys(t *testing.T) {
	testenv.Network(t, "sts.us-east-1.amazonaws.com:443")
	_, err := svc(t, "aws").Test(ctx, Creds{"AWS_ACCESS_KEY_ID": "AKIAIOSFODNN7EXAMPLE", "AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "AWS_DEFAULT_REGION": "us-east-1"})
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
}

func TestAWSWithRealKeys(t *testing.T) {
	id := testenv.Env(t, "SYNTAXIS_TEST_AWS_ACCESS_KEY_ID", "the AWS connection")
	secret := testenv.Env(t, "SYNTAXIS_TEST_AWS_SECRET_ACCESS_KEY", "the AWS connection")
	got, err := svc(t, "aws").Test(ctx, Creds{"AWS_ACCESS_KEY_ID": id, "AWS_SECRET_ACCESS_KEY": secret, "AWS_DEFAULT_REGION": "us-east-1"})
	if err != nil || got.Account == "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAzureWithRealCredentials(t *testing.T) {
	c := Creds{}
	for _, k := range []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET", "AZURE_SUBSCRIPTION_ID"} {
		c[k] = testenv.Env(t, "SYNTAXIS_TEST_"+k, "the Azure connection")
	}
	if id, err := svc(t, "azure").Test(ctx, c); err != nil || id.Account == "" {
		t.Fatalf("%+v %v", id, err)
	}
}

func TestGCPWithRealCredentials(t *testing.T) {
	c := Creds{
		"GCP_SERVICE_ACCOUNT_JSON": testenv.Env(t, "SYNTAXIS_TEST_GCP_SERVICE_ACCOUNT_JSON", "the Google Cloud connection"),
		"GOOGLE_CLOUD_PROJECT":     testenv.Env(t, "SYNTAXIS_TEST_GOOGLE_CLOUD_PROJECT", "the Google Cloud connection"),
	}
	if id, err := svc(t, "gcp").Test(ctx, c); err != nil || id.Account == "" {
		t.Fatalf("%+v %v", id, err)
	}
}

// A real Gitea server in a container: the whole repository workflow, end to end.
func TestGiteaAgainstARealServer(t *testing.T) {
	gs := testenv.Gitea(t)
	base := gs.URL
	g := git(t, "gitea")
	c := Creds{"GITEA_TOKEN": gs.Token, "GITEA_URL": base}
	if id, err := g.Test(ctx, c); err != nil || id.Account != "tester" {
		t.Fatalf("%+v %v", id, err)
	}
	if _, err := g.Test(ctx, Creds{"GITEA_TOKEN": "wrong", "GITEA_URL": base}); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := g.Test(ctx, Creds{"GITEA_TOKEN": gs.Token}); err == nil {
		t.Fatal("a missing server address was accepted")
	}
	repo, err := g.CreateRepo(ctx, c, NewRepo{Name: "app", Description: "d", Private: true})
	if err != nil || repo.FullName != "tester/app" {
		t.Fatalf("%+v %v", repo, err)
	}
	rs, err := g.ListRepos(ctx, c, "")
	if err != nil || len(rs) != 1 || rs[0].FullName != "tester/app" {
		t.Fatalf("%+v %v", rs, err)
	}
	// give the repository a commit on main and a branch with a change, as an agent's work would be
	content := base64.StdEncoding.EncodeToString([]byte("one\n"))
	if code, b := gs.Do(t, "POST", "/api/v1/repos/tester/app/contents/a.txt", map[string]any{"content": content, "message": "first", "branch": "main"}); code != 201 {
		t.Fatalf("first commit: %d %s", code, b)
	}
	if code, b := gs.Do(t, "POST", "/api/v1/repos/tester/app/contents/b.txt", map[string]any{"content": content, "message": "second", "branch": "main", "new_branch": "syntaxis/x"}); code != 201 {
		t.Fatalf("branch: %d %s", code, b)
	}
	// reading a file from the repository, on the default branch and on another
	if b, err := g.ReadFile(ctx, c, "tester/app", "", "a.txt"); err != nil || string(b) != "one\n" {
		t.Fatalf("%q %v", b, err)
	}
	if b, err := g.ReadFile(ctx, c, "tester/app", "syntaxis/x", "b.txt"); err != nil || string(b) != "one\n" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := g.ReadFile(ctx, c, "tester/app", "", "nope.txt"); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("a missing file: %v", err)
	}
	if _, err := g.ReadFile(ctx, c, "tester/app", "", "../etc/passwd"); err == nil {
		t.Fatal("a path outside the repository was accepted")
	}
	u, err := g.OpenPR(ctx, c, PullRequest{Repo: "tester/app", Title: "Add b", Body: "from the agent", Head: "syntaxis/x", Base: "main"})
	if err != nil || !strings.Contains(u, "/tester/app/pulls/") {
		t.Fatalf("%q %v", u, err)
	}
}

// The AWS "get-vanilla" example from AWS's own Signature Version 4 test suite.
func TestSigV4MatchesAWSTestVector(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	signV4(req, nil, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "", "us-east-1", "service", time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestRegistryAndValidate(t *testing.T) {
	r := New(nil)
	ids := map[string]bool{}
	for _, k := range r.Kinds() {
		ids[k.ID] = true
		for _, f := range k.Fields {
			if f.Name == "" || f.Label == "" {
				t.Errorf("%s: incomplete field %+v", k.ID, f)
			}
		}
	}
	for _, want := range []string{"github", "gitlab", "gitea", "cloudflare", "aws", "azure", "gcp", "ifttt"} {
		if !ids[want] {
			t.Errorf("missing %s", want)
		}
	}
	if _, ok := r.Git("aws"); ok {
		t.Error("aws has no repositories")
	}
	if _, ok := r.Git("github"); !ok {
		t.Error("github has repositories")
	}
	k, _ := r.Get("github")
	if Validate(k.Kind(), Creds{}) == nil || Validate(k.Kind(), Creds{"GITHUB_TOKEN": "a\nb"}) == nil || Validate(k.Kind(), Creds{"GITHUB_TOKEN": "ok"}) != nil {
		t.Error("validation")
	}
}
