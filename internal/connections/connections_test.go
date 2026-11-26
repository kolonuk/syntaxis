// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/safehttp"
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

func TestGitLabRejectsAWrongToken(t *testing.T) {
	testenv.Network(t, "gitlab.com:443")
	_, err := svc(t, "gitlab").Test(ctx, Creds{"GITLAB_TOKEN": "glpat-0000000000000000000x"})
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
}

func TestCloudflareRejectsAWrongToken(t *testing.T) {
	testenv.Network(t, "api.cloudflare.com:443")
	_, err := svc(t, "cloudflare").Test(ctx, Creds{"CLOUDFLARE_API_TOKEN": "0000000000000000000000000000000000000000"})
	if err != nil && strings.Contains(err.Error(), "HTTP 429") {
		// Cloudflare rate-limits wrong tokens from shared addresses such as CI runners
		testenv.NotTested(t, "Cloudflare is limiting requests from this address; try again later")
	}
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
}

func TestCloudflareVerifiesAnAccountTokenThroughItsAccount(t *testing.T) {
	const account = "0123456789abcdef0123456789abcdef"
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"tok1","status":"active"}}`))
	}))
	defer srv.Close()
	c := &Cloudflare{HC: srv.Client(), APIBase: srv.URL}
	if _, err := c.Test(ctx, Creds{"CLOUDFLARE_API_TOKEN": "x"}); err != nil || path != "/user/tokens/verify" {
		t.Fatalf("user token: %q %v", path, err)
	}
	if _, err := c.Test(ctx, Creds{"CLOUDFLARE_API_TOKEN": "x", "CLOUDFLARE_ACCOUNT_ID": account}); err != nil || path != "/accounts/"+account+"/tokens/verify" {
		t.Fatalf("account token: %q %v", path, err)
	}
	if _, err := c.Test(ctx, Creds{"CLOUDFLARE_API_TOKEN": "x", "CLOUDFLARE_ACCOUNT_ID": "../../x"}); err == nil {
		t.Fatal("an account ID that is not 32 hex characters was used in a URL")
	}
}

func TestIFTTTRejectsAWrongKey(t *testing.T) {
	testenv.Network(t, "maker.ifttt.com:443")
	_, err := svc(t, "ifttt").Test(ctx, Creds{"IFTTT_WEBHOOK_KEY": "nope"})
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
}

func TestAWSRejectsWrongKeys(t *testing.T) {
	testenv.Network(t, "sts.us-east-1.amazonaws.com:443")
	_, err := svc(t, "aws").Test(ctx, Creds{"AWS_ACCESS_KEY_ID": "AKIAIOSFODNN7EXAMPLE", "AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "AWS_DEFAULT_REGION": "us-east-1"})
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
}

// A real Gitea server in a container: the whole repository workflow, end to end.
func TestGiteaAgainstARealServer(t *testing.T) {
	gs := testenv.Gitea(t)
	base := gs.URL
	// the test server listens on loopback, which the server refuses unless it is allowed explicitly
	g, _ := New(safehttp.NewClient(15*time.Second, netip.MustParsePrefix("127.0.0.0/8"))).Git("gitea")
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

// The server calls the addresses users type for self-hosted services. Those must not reach the
// server's own loopback (the admin app), the LAN, Docker networks or cloud metadata, unless an
// operator allowed a private range; and the address must not carry tricks.
func TestSelfHostedAddressesCannotReachInternalServices(t *testing.T) {
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal secret page", http.StatusTeapot)
	}))
	defer inner.Close()
	for _, kind := range []string{"gitea", "gitlab"} {
		field := map[string]string{"gitea": "GITEA_URL", "gitlab": "GITLAB_HOST"}[kind]
		token := map[string]string{"gitea": "GITEA_TOKEN", "gitlab": "GITLAB_TOKEN"}[kind]
		_, err := svc(t, kind).Test(ctx, Creds{field: inner.URL, token: "x"})
		if err == nil || strings.Contains(err.Error(), "internal secret") || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s reached a loopback service: %v", kind, err)
		}
		for _, bad := range []string{"http://169.254.169.254", "ftp://x.example.com", "https://user:pw@x.example.com", "https://x.example.com/?a=1", "https://x.example.com/#frag"} {
			k, _ := New(nil).Get(kind)
			if err := Validate(k.Kind(), Creds{field: bad, token: "x"}); err == nil && !strings.HasPrefix(bad, "http://169") {
				t.Errorf("%s accepted the address %q", kind, bad)
			}
		}
		if _, err := svc(t, kind).Test(ctx, Creds{field: "http://169.254.169.254", token: "x"}); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s reached the metadata address: %v", kind, err)
		}
	}
	if safehttp.Check(netip.MustParseAddr("127.0.0.1"), nil) == "" || safehttp.Check(netip.MustParseAddr("169.254.169.254"), []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16")}) == "" {
		t.Fatal("loopback must be refused by default and link-local always")
	}
	if safehttp.Check(netip.MustParseAddr("192.168.5.5"), []netip.Prefix{netip.MustParsePrefix("192.168.5.0/24")}) != "" {
		t.Fatal("an allowed private range was refused")
	}
}
