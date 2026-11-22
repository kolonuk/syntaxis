// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

func TestExpandCommand(t *testing.T) {
	cmds := []Command{
		{Name: "review", Template: "Review the changes. $ARGUMENTS"},
		{Name: "fix", Template: "Fix the bug described here:"},
		{Name: "twice", Template: "$ARGUMENTS and again $ARGUMENTS"},
	}
	cases := []struct{ in, want string }{
		{"/review the login code", "Review the changes. the login code"},
		{"/review", "Review the changes."},
		{"  /REVIEW  spaced  ", "Review the changes. spaced"},
		{"/fix it crashes on start", "Fix the bug described here:\n\nit crashes on start"},
		{"/fix", "Fix the bug described here:"},
		{"/twice x", "x and again x"},
		{"/review\nsecond line", "Review the changes. second line"},
	}
	for _, c := range cases {
		if got, cmd := expandCommand(c.in, cmds); cmd == nil || got != c.want {
			t.Errorf("%q: %q %v", c.in, got, cmd)
		}
	}
	// anything else is an ordinary message
	for _, in := range []string{"hello", "/etc/passwd is missing", "/unknown thing", "/", "review the code /review", "//review"} {
		if got, cmd := expandCommand(in, cmds); cmd != nil || got != in {
			t.Errorf("%q was treated as a command: %q", in, got)
		}
	}
}

func TestValidateCommands(t *testing.T) {
	ok := []Command{{Name: "/Deploy-Check", Description: "  a \n b ", Template: "check"}}
	if err := validateCommands(ok); err != nil || ok[0].Name != "deploy-check" || ok[0].Description != "a b" {
		t.Fatalf("%v %+v", err, ok)
	}
	many := make([]Command, maxUserCommands+1)
	for i := range many {
		many[i] = Command{Name: "c" + strings.Repeat("a", i%20) + string(rune('a'+i/20)), Template: "x"}
	}
	for name, cs := range map[string][]Command{
		"bad name":    {{Name: "has space", Template: "x"}},
		"starts with": {{Name: "1abc", Template: "x"}},
		"empty name":  {{Name: "", Template: "x"}},
		"long name":   {{Name: strings.Repeat("a", 40), Template: "x"}},
		"duplicate":   {{Name: "a", Template: "x"}, {Name: "A", Template: "y"}},
		"no text":     {{Name: "a", Template: "  "}},
		"long text":   {{Name: "a", Template: strings.Repeat("x", maxCommandText+1)}},
		"many":        many,
	} {
		if err := validateCommands(cs); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestParseCommandFile(t *testing.T) {
	c := parseCommandFile("lint", "description: Run the linter\n\nRun golangci-lint and fix what it finds. $ARGUMENTS\n")
	if c.Description != "Run the linter" || !strings.HasPrefix(c.Template, "Run golangci-lint") || c.Source != "repository" {
		t.Fatalf("%+v", c)
	}
	c = parseCommandFile("plain", "Summarise the open TODOs in this repository.\nBe brief.")
	if c.Description != "Summarise the open TODOs in this repository." || !strings.Contains(c.Template, "Be brief.") {
		t.Fatalf("%+v", c)
	}
}

func TestCommandsAPI(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	var sent msgLog
	e.app.RunAgent = func(_ context.Context, _ *Run, _ *store.ChatSession, _ *store.User, msg string) {
		sent.add(msg)
	}
	u := e.userSession("c1@example.com")
	other := e.userSession("c2@example.com")
	if code, _, _ := e.client(e.user.URL).do("GET", "/api/commands", nil); code != 401 {
		t.Fatal("anonymous")
	}
	if code, b, _ := u.do("PUT", "/api/commands", map[string]any{"commands": []Command{{Name: "ship", Description: "Ship it", Template: "Run the tests, then open a PR. $ARGUMENTS"}, {Name: "review", Template: "My own review prompt"}}}); code != 200 {
		t.Fatalf("%d %v", code, b)
	}
	if code, _, _ := u.do("PUT", "/api/commands", map[string]any{"commands": []Command{{Name: "bad name", Template: "x"}}}); code != 400 {
		t.Fatalf("%d", code)
	}
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	_, _, raw := u.do("GET", "/api/sessions/"+id+"/commands", nil)
	if !strings.Contains(string(raw), `"name":"ship"`) || !strings.Contains(string(raw), `"name":"test"`) || strings.Contains(string(raw), "Run the tests, then open") {
		t.Fatalf("the list should name the commands and not carry their text: %s", raw)
	}
	// the user's own command wins over the built-in one of the same name
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/messages", map[string]any{"text": "/review please"}); code != 202 {
		t.Fatal(code)
	}
	waitFor(t, func() bool { return sent.n() == 1 })
	if sent.at(0) != "My own review prompt\n\nplease" {
		t.Fatalf("%q", sent.at(0))
	}
	// another user has only the built-in commands
	_, s2, _ := other.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	if _, _, raw := other.do("GET", "/api/sessions/"+s2["id"].(string)+"/commands", nil); strings.Contains(string(raw), `"name":"ship"`) {
		t.Fatalf("commands leaked to another user: %s", raw)
	}
}

func TestHooksSettingIsValidatedAndPrivate(t *testing.T) {
	e := newEnv(t)
	u := e.userSession("h1@example.com")
	other := e.userSession("h2@example.com")
	good := `{"hooks":[{"event":"post_tool","tools":["write_file"],"command":"gofmt -l ."}]}`
	if code, _, _ := u.do("PUT", "/api/hooks", map[string]string{"hooks": good}); code != 200 {
		t.Fatal("save")
	}
	for _, bad := range []string{`{`, `{"hooks":[{"event":"x","command":"true"}]}`, `{"hooks":[{"event":"pre_tool","command":""}]}`} {
		if code, b, _ := u.do("PUT", "/api/hooks", map[string]string{"hooks": bad}); code != 400 || b["error"] == nil {
			t.Errorf("%q: %d", bad, code)
		}
	}
	if _, b, _ := u.do("GET", "/api/hooks", nil); b["hooks"] != good {
		t.Fatalf("%v", b)
	}
	if _, b, _ := other.do("GET", "/api/hooks", nil); b["hooks"] != "" {
		t.Fatalf("hooks leaked to another user: %v", b)
	}
	uid := userID(t, e, "h1@example.com")
	notes := ""
	hs := e.app.hooksFor(context.Background(), uid, "repo", "", execFS{exec: func(context.Context, sandbox.ExecSpec) (sandbox.ExecResult, error) {
		return sandbox.ExecResult{ExitCode: 1}, nil
	}}, func(m string) { notes += m })
	if len(hs) != 1 || hs[0].Source != "user" || notes != "" {
		t.Fatalf("%+v %q", hs, notes)
	}
}

// Repository commands and hooks, read from a real workspace.
func TestIntegrationRepositoryCommandsAndHooks(t *testing.T) {
	e := newEnv(t)
	_ = e.st.SetSetting(context.Background(), SettingUploadsEnabled, "true")
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	u := e.userSession("rc@example.com")
	uid := userID(t, e, "rc@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	ctx := context.Background()
	t.Cleanup(func() {
		if cur, err := e.st.GetChatSession(ctx, id); err == nil && cur.SandboxID != "" {
			_ = e.app.Sandbox.Destroy(ctx, id, cur.SandboxID)
		}
		_ = e.app.Sandbox.RT.RemoveVolume(ctx, sandbox.WorkspaceVolume(uid))
	})
	up := func(p, content string) {
		t.Helper()
		if code, _, raw := u.do("PUT", "/api/sessions/"+id+"/files/content", map[string]string{"path": p, "text": content}); code != 200 {
			// the first write has to create the sandbox: upload does that
			if c2, _, r2 := u.doRaw("POST", "/api/sessions/"+id+"/files/upload?dir="+p[:strings.LastIndex(p, "/")]+"&name="+p[strings.LastIndex(p, "/")+1:], "application/octet-stream", []byte(content)); c2 != 200 {
				t.Fatalf("%s: %d %s / %d %s", p, code, raw, c2, r2)
			}
		}
	}
	up("/workspace/.syntaxis/commands/lint.md", "description: Run the linter\n\nRun the linter and fix what it finds. $ARGUMENTS\n")
	up("/workspace/.syntaxis/commands/Bad Name.md", "never listed")
	up("/workspace/.syntaxis/commands/review.md", "The repository's own review rules. $ARGUMENTS")
	up("/workspace/.syntaxis/hooks.json", `{"hooks":[{"event":"post_tool","tools":["write_file"],"command":"echo checked"}]}`)

	_, _, raw := u.do("GET", "/api/sessions/"+id+"/commands", nil)
	if !strings.Contains(string(raw), `"name":"lint"`) || !strings.Contains(string(raw), `"source":"repository"`) || strings.Contains(string(raw), "Bad Name") {
		t.Fatalf("%s", raw)
	}
	cur, _ := e.st.GetChatSession(ctx, id)
	var review Command
	for _, c := range e.app.commandsFor(ctx, cur, uid) {
		if c.Name == "review" {
			review = c
		}
	}
	if review.Source != "repository" || !strings.Contains(review.Template, "repository's own review rules") {
		t.Fatalf("a repository command should win over the built-in one: %+v", review)
	}
	// hooks from the file
	exec := func(c context.Context, sp sandbox.ExecSpec) (sandbox.ExecResult, error) {
		if sp.WorkingDir == "" {
			sp.WorkingDir = "/"
		}
		return e.app.Sandbox.Exec(c, id, cur.SandboxID, sp)
	}
	var warned []string
	hs := e.app.hooksFor(ctx, uid, repoHooksKey(cur), "", execFS{exec: exec}, func(m string) { warned = append(warned, m) })
	if len(hs) != 0 || len(warned) != 1 || !strings.Contains(warned[0], "not run until you trust") {
		t.Fatalf("repository hooks ran without being trusted: %+v %v", hs, warned)
	}
	// the user reads the file and trusts exactly that content
	base := "/api/sessions/" + id
	_, rh, _ := u.do("GET", base+"/repo-hooks", nil)
	if rh["present"] != true || rh["trusted"] != false || rh["hash"] == "" {
		t.Fatalf("%v", rh)
	}
	if code, _, _ := u.do("POST", base+"/repo-hooks/trust", map[string]string{"hash": "0000"}); code != 409 {
		t.Fatalf("trusting a hash that is not the file: %d", code)
	}
	if code, _, raw := u.do("POST", base+"/repo-hooks/trust", map[string]string{"hash": rh["hash"].(string)}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	warned = nil
	hs = e.app.hooksFor(ctx, uid, repoHooksKey(cur), "", execFS{exec: exec}, func(m string) { warned = append(warned, m) })
	if len(hs) != 1 || hs[0].Source != "repository" || hs[0].Command != "echo checked" || len(warned) != 0 {
		t.Fatalf("%+v %v", hs, warned)
	}
	// trust belongs to this user and this repository
	if hs := e.app.hooksFor(ctx, uid, "another-repo", "", execFS{exec: exec}, func(string) {}); len(hs) != 0 {
		t.Fatalf("trust carried over to another repository: %+v", hs)
	}
	if hs := e.app.hooksFor(ctx, uid+1, repoHooksKey(cur), "", execFS{exec: exec}, func(string) {}); len(hs) != 0 {
		t.Fatalf("trust carried over to another user: %+v", hs)
	}
	// the admin can switch repository hooks and commands off
	_ = e.st.SetSetting(ctx, "repo_hooks", "false")
	if hs := e.app.hooksFor(ctx, uid, repoHooksKey(cur), "", execFS{exec: exec}, func(string) {}); len(hs) != 0 {
		t.Fatalf("repository hooks ran although they are switched off: %+v", hs)
	}
	_ = e.st.SetSetting(ctx, "repo_hooks", "true")
	// a broken file is reported and ignored
	up("/workspace/.syntaxis/hooks.json", `{"hooks":[{"event":"never","command":"true"}]}`)
	warned = nil
	if hs := e.app.hooksFor(ctx, uid, repoHooksKey(cur), "", execFS{exec: exec}, func(m string) { warned = append(warned, m) }); len(hs) != 0 || len(warned) != 1 || !strings.Contains(warned[0], "ignored") {
		t.Fatalf("%+v %v", hs, warned)
	}
}
