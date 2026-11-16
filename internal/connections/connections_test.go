// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

func srv(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestGitHub(t *testing.T) {
	s := srv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"message":"Bad credentials"}`, 401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /user":
			_, _ = io.WriteString(w, `{"login":"octo","name":"Octo Cat"}`)
		case "GET /user/repos":
			_, _ = io.WriteString(w, `[{"full_name":"octo/app","description":"the app","private":true,"clone_url":"https://x/octo/app.git","default_branch":"main"},{"full_name":"octo/docs","description":"","private":false}]`)
		case "POST /user/repos":
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), `"name":"new"`) || !strings.Contains(string(b), `"private":true`) {
				t.Errorf("create body %s", b)
			}
			_, _ = io.WriteString(w, `{"full_name":"octo/new","private":true,"clone_url":"https://x/octo/new.git","default_branch":"main"}`)
		case "GET /repos/octo/app":
			_, _ = io.WriteString(w, `{"default_branch":"trunk"}`)
		case "POST /repos/octo/app/pulls":
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), `"base":"trunk"`) || !strings.Contains(string(b), `"head":"feature"`) {
				t.Errorf("pr body %s", b)
			}
			_, _ = io.WriteString(w, `{"html_url":"https://github.example/octo/app/pull/7"}`)
		default:
			http.NotFound(w, r)
		}
	})
	g := &GitHub{HC: s.Client(), APIBase: s.URL}
	c := Creds{"GITHUB_TOKEN": "tok"}
	id, err := g.Test(ctx, c)
	if err != nil || id.Account != "octo" {
		t.Fatalf("%+v %v", id, err)
	}
	if _, err := g.Test(ctx, Creds{"GITHUB_TOKEN": "bad"}); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a refused token must say so: %v", err)
	}
	rs, _ := g.ListRepos(ctx, c, "")
	if len(rs) != 2 {
		t.Fatalf("%+v", rs)
	}
	if rs, _ := g.ListRepos(ctx, c, "APP"); len(rs) != 1 || rs[0].FullName != "octo/app" {
		t.Fatalf("search: %+v", rs)
	}
	if r, err := g.CreateRepo(ctx, c, NewRepo{Name: "new", Private: true}); err != nil || r.FullName != "octo/new" {
		t.Fatalf("%+v %v", r, err)
	}
	if u, err := g.OpenPR(ctx, c, PullRequest{Repo: "octo/app", Title: "t", Head: "feature"}); err != nil || !strings.HasSuffix(u, "/pull/7") {
		t.Fatalf("%q %v", u, err)
	}
	if _, err := g.OpenPR(ctx, c, PullRequest{Repo: "../etc/passwd", Head: "x"}); err == nil {
		t.Fatal("a repository name that is not owner/name was accepted")
	}
}

func TestTokenNeverAppearsInErrors(t *testing.T) {
	s := srv(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 500) })
	g := &GitHub{HC: s.Client(), APIBase: s.URL}
	_, err := g.Test(ctx, Creds{"GITHUB_TOKEN": "ghp_supersecretvalue"})
	if err == nil || strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("%v", err)
	}
}

func TestGitLab(t *testing.T) {
	s := srv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			http.Error(w, "{}", 401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v4/user":
			_, _ = io.WriteString(w, `{"username":"gl","name":"G L"}`)
		case "GET /api/v4/projects":
			if r.URL.Query().Get("search") != "app" {
				t.Errorf("search %q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `[{"path_with_namespace":"grp/sub/app","visibility":"private","http_url_to_repo":"https://gl/grp/sub/app.git","default_branch":"main"}]`)
		case "POST /api/v4/projects":
			_, _ = io.WriteString(w, `{"path_with_namespace":"gl/new","visibility":"private"}`)
		case "GET /api/v4/projects/grp%2Fsub%2Fapp", "GET /api/v4/projects/grp/sub/app":
			_, _ = io.WriteString(w, `{"default_branch":"main"}`)
		case "POST /api/v4/projects/grp%2Fsub%2Fapp/merge_requests", "POST /api/v4/projects/grp/sub/app/merge_requests":
			_, _ = io.WriteString(w, `{"web_url":"https://gl/grp/sub/app/-/merge_requests/3"}`)
		default:
			http.NotFound(w, r)
		}
	})
	g := &GitLab{HC: s.Client()}
	c := Creds{"GITLAB_TOKEN": "tok", "GITLAB_HOST": s.URL + "/"}
	if id, err := g.Test(ctx, c); err != nil || id.Account != "gl" {
		t.Fatalf("%+v %v", id, err)
	}
	if rs, err := g.ListRepos(ctx, c, "app"); err != nil || len(rs) != 1 || !rs[0].Private || rs[0].FullName != "grp/sub/app" {
		t.Fatalf("%+v %v", rs, err)
	}
	if r, err := g.CreateRepo(ctx, c, NewRepo{Name: "new", Private: true}); err != nil || r.FullName != "gl/new" {
		t.Fatalf("%+v %v", r, err)
	}
	if u, err := g.OpenPR(ctx, c, PullRequest{Repo: "grp/sub/app", Title: "t", Head: "f"}); err != nil || !strings.Contains(u, "merge_requests/3") {
		t.Fatalf("%q %v", u, err)
	}
	if _, err := g.Test(ctx, Creds{"GITLAB_TOKEN": "tok", "GITLAB_HOST": "ftp://x"}); err == nil {
		t.Fatal("a non-http address was accepted")
	}
}

func TestGitea(t *testing.T) {
	s := srv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token tok" {
			http.Error(w, "{}", 401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/user":
			_, _ = io.WriteString(w, `{"login":"ge"}`)
		case "GET /api/v1/user/repos":
			_, _ = io.WriteString(w, `[{"full_name":"ge/app","private":true,"clone_url":"https://g/ge/app.git","default_branch":"main"}]`)
		case "POST /api/v1/user/repos":
			_, _ = io.WriteString(w, `{"full_name":"ge/new"}`)
		case "POST /api/v1/repos/ge/app/pulls":
			_, _ = io.WriteString(w, `{"html_url":"https://g/ge/app/pulls/2"}`)
		default:
			http.NotFound(w, r)
		}
	})
	g := &Gitea{HC: s.Client()}
	c := Creds{"GITEA_TOKEN": "tok", "GITEA_URL": s.URL}
	if id, err := g.Test(ctx, c); err != nil || id.Account != "ge" {
		t.Fatalf("%+v %v", id, err)
	}
	if rs, _ := g.ListRepos(ctx, c, ""); len(rs) != 1 {
		t.Fatalf("%+v", rs)
	}
	if u, err := g.OpenPR(ctx, c, PullRequest{Repo: "ge/app", Title: "t", Head: "f", Base: "main"}); err != nil || !strings.HasSuffix(u, "/pulls/2") {
		t.Fatalf("%q %v", u, err)
	}
	if _, err := g.Test(ctx, Creds{"GITEA_TOKEN": "tok"}); err == nil {
		t.Fatal("a missing server address was accepted")
	}
}

func TestCloudflare(t *testing.T) {
	s := srv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "{}", 401)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"result":{"id":"abc123","status":"active"}}`)
	})
	cf := &Cloudflare{HC: s.Client(), APIBase: s.URL}
	if id, err := cf.Test(ctx, Creds{"CLOUDFLARE_API_TOKEN": "tok"}); err != nil || !strings.Contains(id.Account, "abc123") {
		t.Fatalf("%+v %v", id, err)
	}
	if _, err := cf.Test(ctx, Creds{"CLOUDFLARE_API_TOKEN": "no"}); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
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

func TestAWS(t *testing.T) {
	s := srv(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=AKIATEST/") || r.Header.Get("X-Amz-Security-Token") != "sess" {
			http.Error(w, "<Error/>", 403)
			return
		}
		_, _ = io.WriteString(w, `<GetCallerIdentityResponse><GetCallerIdentityResult><Arn>arn:aws:iam::111122223333:user/me</Arn><Account>111122223333</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`)
	})
	a := &AWS{HC: s.Client(), Endpoint: s.URL + "/", Now: func() time.Time { return time.Unix(1700000000, 0) }}
	id, err := a.Test(ctx, Creds{"AWS_ACCESS_KEY_ID": "AKIATEST", "AWS_SECRET_ACCESS_KEY": "s", "AWS_SESSION_TOKEN": "sess"})
	if err != nil || id.Account != "111122223333" || !strings.Contains(id.Detail, "user/me") {
		t.Fatalf("%+v %v", id, err)
	}
	if _, err := a.Test(ctx, Creds{"AWS_ACCESS_KEY_ID": "AKIAOTHER", "AWS_SECRET_ACCESS_KEY": "s"}); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
}

func TestAzure(t *testing.T) {
	const tenant, sub = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	var arm *httptest.Server
	login := srv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/"+tenant+"/oauth2/v2.0/token" || r.Form.Get("client_secret") != "shh" {
			http.Error(w, `{"error":"invalid_client"}`, 401)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"armtoken"}`)
	})
	arm = srv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer armtoken" || r.URL.Path != "/subscriptions/"+sub {
			http.Error(w, "{}", 403)
			return
		}
		_, _ = io.WriteString(w, `{"displayName":"My Sub","state":"Enabled"}`)
	})
	az := &Azure{HC: http.DefaultClient, LoginBase: login.URL, ARMBase: arm.URL}
	c := Creds{"AZURE_TENANT_ID": tenant, "AZURE_CLIENT_ID": "cid", "AZURE_CLIENT_SECRET": "shh", "AZURE_SUBSCRIPTION_ID": sub}
	if id, err := az.Test(ctx, c); err != nil || id.Account != "My Sub" {
		t.Fatalf("%+v %v", id, err)
	}
	c["AZURE_CLIENT_SECRET"] = "wrong"
	if _, err := az.Test(ctx, c); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("%v", err)
	}
	c["AZURE_TENANT_ID"] = "../../x"
	if _, err := az.Test(ctx, c); err == nil {
		t.Fatal("a tenant that is not an ID was accepted")
	}
}

func TestGCP(t *testing.T) {
	pk, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(pk)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	keyJSON, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "sa@proj.iam.gserviceaccount.com", "private_key": pemKey})
	tokenSrv := srv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) != 3 {
			http.Error(w, "{}", 400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&pk.PublicKey, crypto.SHA256, h[:], sig) != nil {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		if !strings.Contains(string(claims), "sa@proj.iam.gserviceaccount.com") || !strings.Contains(string(claims), "cloud-platform") {
			http.Error(w, "{}", 400)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"gtok"}`)
	})
	crm := srv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gtok" || r.URL.Path != "/v1/projects/my-proj" {
			http.Error(w, "{}", 403)
			return
		}
		_, _ = io.WriteString(w, `{"projectId":"my-proj"}`)
	})
	g := &GCP{HC: http.DefaultClient, TokenURL: tokenSrv.URL, CRMBase: crm.URL}
	id, err := g.Test(ctx, Creds{"GCP_SERVICE_ACCOUNT_JSON": string(keyJSON), "GOOGLE_CLOUD_PROJECT": "my-proj"})
	if err != nil || id.Account != "sa@proj.iam.gserviceaccount.com" || !strings.Contains(id.Detail, "my-proj") {
		t.Fatalf("%+v %v", id, err)
	}
	for _, bad := range []string{"not json", `{"type":"user"}`, `{"type":"service_account","client_email":"a","private_key":"junk"}`} {
		if _, err := g.Test(ctx, Creds{"GCP_SERVICE_ACCOUNT_JSON": bad, "GOOGLE_CLOUD_PROJECT": "p"}); err == nil {
			t.Errorf("accepted %q", bad)
		}
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
	for _, want := range []string{"github", "gitlab", "gitea", "cloudflare", "aws", "azure", "gcp"} {
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
