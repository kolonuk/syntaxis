// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

// buildTemplateImage builds a real environment image on top of the sandbox image, with a file that
// shows which image a sandbox really runs.
func buildTemplateImage(t *testing.T, tag, marker string) {
	t.Helper()
	dir := t.TempDir()
	df := "FROM " + sandbox.DefaultConfig().Image + "\nUSER 0\nRUN echo " + marker + " > /etc/syntaxis-environment\nUSER 10001:10001\n"
	if err := writeFile(dir+"/Dockerfile", df); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "build", "-q", "-t", tag, dir).CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
}

// An approved environment is a real image: a session that picks it runs in it, and only an admin
// can approve one.
func TestEnvironmentsAreApprovedByAdminsAndUsedByRealSandboxes(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
	buildTemplateImage(t, "syntaxis-test-env:one", "environment-one")
	usr := e.userSession("a@example.com")
	adm := e.makeAdminSession()

	if code, _, _ := usr.do("PUT", "/api/image-templates/one", map[string]any{"image": "syntaxis-test-env:one"}); code == 200 {
		t.Fatal("a chat user approved an environment")
	}
	for name, body := range map[string]map[string]any{
		"Bad Name":  {"image": "syntaxis-test-env:one"},
		"one":       {"image": "has space"},
		"also-good": {"image": "x", "matches": []string{"bad ref!"}},
	} {
		if code, _, _ := adm.do("PUT", "/api/image-templates/"+strings.ReplaceAll(name, " ", "-"), body); code != 400 && name != "one" {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, m, raw := adm.do("PUT", "/api/image-templates/one", map[string]any{"image": "syntaxis-test-env:one", "description": "Test environment"}); code != 200 || m["present"] != true {
		t.Fatalf("%d %s", code, raw)
	}
	// one whose image is not built is listed for the admin but not offered to users
	adm.do("PUT", "/api/image-templates/unbuilt", map[string]any{"image": "syntaxis-test-env:never-built"})
	_, _, raw := usr.do("GET", "/api/environments", nil)
	if !strings.Contains(string(raw), `"one"`) || strings.Contains(string(raw), "unbuilt") {
		t.Fatalf("users see the built environments only: %s", raw)
	}
	_, _, raw = adm.do("GET", "/api/image-templates", nil)
	if !strings.Contains(string(raw), `"present":false`) {
		t.Fatalf("the admin must see that an image is missing: %s", raw)
	}

	code, s, raw2 := usr.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "template": "one"})
	if code != 201 || s["environment"].(map[string]any)["image"] != "syntaxis-test-env:one" {
		t.Fatalf("%d %s", code, raw2)
	}
	if code, _, _ := usr.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "template": "cobol"}); code != 400 {
		t.Fatal("an environment nobody approved was accepted")
	}
	_, s2, _ := usr.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "template": "unbuilt"})
	if env := s2["environment"].(map[string]any); env["image"] != "" || !strings.Contains(env["note"].(string), "not built") {
		t.Fatalf("an unbuilt environment falls back to the default with a note: %v", env)
	}

	// the sandbox really runs that image
	ctx := context.Background()
	id := s["id"].(string)
	sess, _ := e.st.GetChatSession(ctx, id)
	usrRow, _ := e.st.GetUser(ctx, sess.UserID)
	cid, err := e.app.ensureSandbox(ctx, sess, usrRow)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.app.Sandbox.Exec(ctx, id, cid, execSpec("cat /etc/syntaxis-environment"))
	if err != nil || strings.TrimSpace(r.Stdout) != "environment-one" {
		t.Fatalf("the sandbox is not running the approved image: %v %+v", err, r)
	}
	if got := dockerInspect(t, cid, "{{.Config.Image}} {{.HostConfig.Runtime}}"); got != "syntaxis-test-env:one runsc" {
		t.Fatalf("%s", got)
	}
}

// A repository's devcontainer.json is honoured only through an approved environment. The files are
// served by a real Gitea and read with the user's own connection.
func TestDevcontainerPicksAnApprovedEnvironmentAndNothingElse(t *testing.T) {
	gs := testenv.Gitea(t)
	e := newEnv(t)
	allowLoopbackConnections(e) // the test Gitea listens on loopback
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	buildTemplateImage(t, "syntaxis-test-env:py", "py")
	usr := e.userSession("a@example.com")
	adm := e.makeAdminSession()
	adm.do("PUT", "/api/image-templates/python", map[string]any{"image": "syntaxis-test-env:py", "matches": []string{"mcr.microsoft.com/devcontainers/python"}})
	if code, _, raw := usr.do("PUT", "/api/connections/gitea", map[string]any{"fields": map[string]string{"GITEA_TOKEN": gs.Token, "GITEA_URL": gs.URL}}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	mkRepo := func(name, devcontainer string) map[string]any {
		if code, _, raw := usr.do("POST", "/api/connections/gitea/repos", map[string]any{"name": name, "private": true}); code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		if devcontainer != "" {
			c := base64.StdEncoding.EncodeToString([]byte(devcontainer))
			if code, b := gs.Do(t, "POST", "/api/v1/repos/"+gs.User+"/"+name+"/contents/.devcontainer/devcontainer.json", map[string]any{"content": c, "message": "dc", "branch": "main"}); code != 201 {
				t.Fatalf("%d %s", code, b)
			}
		}
		return map[string]any{"kind": "gitea", "full_name": gs.User + "/" + name, "clone_url": gs.URL + "/" + gs.User + "/" + name + ".git", "default_branch": "main"}
	}
	env := func(repo map[string]any) map[string]any {
		code, s, raw := usr.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "repo": repo})
		if code != 201 {
			t.Fatalf("%d %s", code, raw)
		}
		return s["environment"].(map[string]any)
	}
	// asks for an image that matches an approved environment (comments in the file and all)
	got := env(mkRepo("py", "// a comment\n{\n  \"image\": \"mcr.microsoft.com/devcontainers/python:3-3.14-trixie\", // more\n  \"postCreateCommand\": \"pip install -r requirements.txt\",\n}\n"))
	if got["image"] != "syntaxis-test-env:py" || !strings.Contains(got["note"].(string), "devcontainer.json") {
		t.Fatalf("%v", got)
	}
	// asks for an image nobody approved: ignored, and the user is told
	got = env(mkRepo("evil", `{"image": "evil.example.net/rootkit:latest"}`))
	if got["image"] != "" || !strings.Contains(got["note"].(string), "not one of the approved") {
		t.Fatalf("%v", got)
	}
	// builds from a Dockerfile: not supported, and said so
	got = env(mkRepo("dockerfile", `{"build": {"dockerfile": "Dockerfile"}}`))
	if got["image"] != "" || !strings.Contains(got["note"].(string), "Dockerfile") {
		t.Fatalf("%v", got)
	}
	// no devcontainer.json at all: the default, quietly
	got = env(mkRepo("plain", ""))
	if got["image"] != "" || got["note"] != "" {
		t.Fatalf("%v", got)
	}
}
