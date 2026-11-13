// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/rules"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/scanner"
)

func newExec() (*Executor, *box) {
	b := newBox()
	return &Executor{Exec: b.exec, Model: "test-model"}, b
}

func run(x *Executor, name, args string) Output {
	return x.Run(context.Background(), name, json.RawMessage(args))
}

func TestSafePath(t *testing.T) {
	ok := map[string]string{"a.txt": "/workspace/a.txt", "/workspace/x/y": "/workspace/x/y", "./b/../c": "/workspace/c", "/workspace": "/workspace"}
	for in, want := range ok {
		if got, err := SafePath(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../../etc/passwd", "/workspace/../etc/shadow", "a\x00b", "/workspacex/file", "/workspace/../../root"} {
		if _, err := SafePath(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestWriteReadPatchRoundTrip(t *testing.T) {
	x, b := newExec()
	if o := run(x, ToolWrite, `{"path":"src/main.go","content":"package main\n\nfunc main() {}\n"}`); o.IsError {
		t.Fatal(o.Text)
	}
	if b.files["/workspace/src/main.go"] == "" {
		t.Fatal("not written")
	}
	if o := run(x, ToolRead, `{"path":"src/main.go"}`); o.Text != b.files["/workspace/src/main.go"] {
		t.Fatalf("%q", o.Text)
	}
	o := run(x, ToolRead, `{"path":"src/main.go","start_line":3,"end_line":3}`)
	if o.Text != "3\tfunc main() {}" {
		t.Fatalf("%q", o.Text)
	}
	if o := run(x, ToolPatch, `{"path":"src/main.go","old":"func main() {}","new":"func main() { println(1) }"}`); o.IsError {
		t.Fatal(o.Text)
	}
	if !strings.Contains(b.files["/workspace/src/main.go"], "println(1)") {
		t.Fatal("patch not applied")
	}
}

func TestPatchFailuresAreInstructive(t *testing.T) {
	x, b := newExec()
	b.files["/workspace/f.txt"] = "a\nb\na\n"
	if o := run(x, ToolPatch, `{"path":"f.txt","old":"zzz","new":"y"}`); !o.IsError || !strings.Contains(o.Text, "not found") {
		t.Fatalf("%+v", o)
	}
	if o := run(x, ToolPatch, `{"path":"f.txt","old":"a","new":"y"}`); !o.IsError || !strings.Contains(o.Text, "occurs 2 times") {
		t.Fatalf("ambiguous patch applied: %+v", o)
	}
	if b.files["/workspace/f.txt"] != "a\nb\na\n" {
		t.Fatal("file modified by a failed patch")
	}
	if o := run(x, ToolPatch, `{"path":"missing.txt","old":"a","new":"b"}`); !o.IsError {
		t.Fatal("missing file")
	}
	if o := run(x, ToolPatch, `{"path":"f.txt","old":"","new":"b"}`); !o.IsError {
		t.Fatal("empty old")
	}
}

func TestToolsRejectPathEscape(t *testing.T) {
	x, b := newExec()
	for name, args := range map[string]string{
		ToolRead:   `{"path":"/etc/passwd"}`,
		ToolWrite:  `{"path":"../../root/.bashrc","content":"x"}`,
		ToolPatch:  `{"path":"/etc/hosts","old":"a","new":"b"}`,
		ToolList:   `{"path":"/"}`,
		ToolSearch: `{"pattern":"x","path":"/etc"}`,
	} {
		if o := run(x, name, args); !o.IsError || !strings.Contains(o.Text, "outside the workspace") {
			t.Errorf("%s escaped: %+v", name, o)
		}
	}
	if len(b.cmds) != 0 {
		t.Fatalf("commands ran for rejected paths: %v", b.cmds)
	}
}

func TestArgumentsPassedAsArgvNotInterpolated(t *testing.T) {
	x, b := newExec()
	run(x, ToolSearch, `{"pattern":"$(rm -rf /); echo","path":"src"}`)
	last := b.cmds[len(b.cmds)-1]
	// the pattern is a positional argument after the fixed script, not part of the script text
	if !strings.Contains(last, `-e "$1"`) {
		t.Fatalf("pattern interpolated into script: %s", last)
	}
	run(x, ToolWrite, `{"path":"a b/c'd.txt","content":"x"}`)
	if !strings.Contains(b.cmds[len(b.cmds)-1], `"$1"`) {
		t.Fatal("path interpolated")
	}
}

func TestSearchNoMatches(t *testing.T) {
	x, b := newExec()
	b.run = func(argv []string) (sandbox.ExecResult, bool) { return sandbox.ExecResult{ExitCode: 1}, true }
	if o := run(x, ToolSearch, `{"pattern":"zzz"}`); o.Text != "no matches" || o.IsError {
		t.Fatalf("%+v", o)
	}
}

func TestShellRequiresCommandAndReportsExit(t *testing.T) {
	x, b := newExec()
	if o := run(x, ToolShell, `{"command":"  "}`); !o.IsError {
		t.Fatal("empty command")
	}
	b.run = func([]string) (sandbox.ExecResult, bool) {
		return sandbox.ExecResult{ExitCode: 2, Stdout: "out", Stderr: "err", Truncated: true}, true
	}
	o := run(x, ToolShell, `{"command":"false"}`)
	if !o.IsError || !strings.Contains(o.Text, "exit code: 2") || !strings.Contains(o.Text, "--- stderr ---") || !strings.Contains(o.Text, "cut at the size cap") {
		t.Fatalf("%q", o.Text)
	}
	b.run = func([]string) (sandbox.ExecResult, bool) { return sandbox.ExecResult{TimedOut: true}, true }
	if o := run(x, ToolShell, `{"command":"sleep 999"}`); !o.IsError || !strings.Contains(o.Text, "timed out") {
		t.Fatalf("%q", o.Text)
	}
}

func TestFetchValidationAndUntrusted(t *testing.T) {
	x, _ := newExec()
	for _, bad := range []string{`{"url":"file:///etc/passwd"}`, `{"url":"ftp://x"}`, `{"url":"javascript:alert(1)"}`, `{"url":"http://"}`} {
		if o := run(x, ToolFetch, bad); !o.IsError {
			t.Errorf("accepted %s", bad)
		}
	}
	if o := run(x, ToolFetch, `{"url":"https://example.com"}`); !o.Untrusted {
		t.Fatal("fetch output must be flagged untrusted")
	}
}

func TestGitInputValidation(t *testing.T) {
	x, b := newExec()
	for _, bad := range []string{
		`{"action":"rebase"}`, `{"action":"checkout","branch":"--orphan"}`, `{"action":"branch","branch":"a;rm -rf /"}`,
		`{"action":"add","paths":["--force"]}`, `{"action":"commit"}`, `{"action":"checkout"}`, `{"action":"pr"}`,
	} {
		if o := run(x, ToolGit, bad); !o.IsError {
			t.Errorf("accepted %s", bad)
		}
	}
	if len(b.cmds) != 0 {
		t.Fatalf("git ran for invalid input: %v", b.cmds)
	}
}

func TestCommitAddsAgentTrailer(t *testing.T) {
	x, b := newExec()
	run(x, ToolGit, `{"action":"commit","message":"feat: add thing"}`)
	last := b.cmds[len(b.cmds)-1]
	if !strings.Contains(last, "Agent-Authored-By: Syntaxis (test-model)") {
		t.Fatalf("no trailer: %s", last)
	}
}

func secretDiff() string {
	return "+++ b/config.env\n+TOKEN=ghp_" + strings.Repeat("aB3dE5gH7j", 4) + "\n"
}

func gitBox(diff string) *box {
	b := newBox()
	b.run = func(argv []string) (sandbox.ExecResult, bool) {
		j := strings.Join(argv, " ")
		switch {
		case strings.HasPrefix(j, "git diff --cached"), strings.HasPrefix(j, "git log -p"):
			return sandbox.ExecResult{Stdout: diff}, true
		case strings.HasPrefix(j, "git rev-parse"):
			return sandbox.ExecResult{Stdout: "feature\n"}, true
		}
		return sandbox.ExecResult{Stdout: "done"}, true
	}
	return b
}

func scanWith(mode scanner.Mode, override bool) func(string, bool) scanner.Decision {
	return func(text string, force bool) scanner.Decision { return scanner.Evaluate(text, mode, override, force) }
}

func TestSecretScannerBlocksPush(t *testing.T) {
	b := gitBox(secretDiff())
	var audits []string
	x := &Executor{Exec: b.exec, Scan: scanWith(scanner.ModeBlock, true), Audit: func(a, d string) { audits = append(audits, a) }}
	o := run(x, ToolGit, `{"action":"push","branch":"feature"}`)
	if !o.Blocked || !o.IsError || !strings.Contains(o.Text, "github-token") {
		t.Fatalf("%+v", o)
	}
	for _, c := range b.cmds {
		if strings.HasPrefix(c, "git push") {
			t.Fatal("push ran despite block")
		}
	}
	if strings.Contains(o.Text, strings.Repeat("aB3dE5gH7j", 4)) {
		t.Fatal("full secret echoed back")
	}
	if len(audits) == 0 || audits[0] != "scan.block" {
		t.Fatalf("%v", audits)
	}
}

func TestSecretScannerUserOverride(t *testing.T) {
	b := gitBox(secretDiff())
	var audits []string
	x := &Executor{Exec: b.exec, Scan: scanWith(scanner.ModeBlock, true), Audit: func(a, d string) { audits = append(audits, a) },
		ConfirmOverride: func(context.Context, scanner.Decision) (bool, error) { return true, nil }}
	// the override is only offered when the Decision allows forcing; with the scanner's own
	// force flag we simulate the confirmed path by re-evaluating with force=true
	x.Scan = func(text string, force bool) scanner.Decision {
		return scanner.Evaluate(text, scanner.ModeBlock, true, false)
	}
	o := run(x, ToolGit, `{"action":"push","branch":"feature"}`)
	if o.IsError {
		t.Fatalf("confirmed override should push: %+v", o)
	}
	pushed := false
	for _, c := range b.cmds {
		if strings.HasPrefix(c, "git push") {
			pushed = true
		}
	}
	if !pushed || len(audits) == 0 || audits[0] != "scan.override" {
		t.Fatalf("pushed=%v audits=%v", pushed, audits)
	}
}

func TestSecretScannerOverrideForbidden(t *testing.T) {
	b := gitBox(secretDiff())
	asked := false
	x := &Executor{Exec: b.exec, Scan: scanWith(scanner.ModeBlock, false),
		ConfirmOverride: func(context.Context, scanner.Decision) (bool, error) { asked = true; return true, nil }}
	o := run(x, ToolGit, `{"action":"push","branch":"feature"}`)
	if !o.Blocked || asked {
		t.Fatalf("admin forbade override but user was offered it: blocked=%v asked=%v", o.Blocked, asked)
	}
}

func TestSecretScannerWarnAllowsAndCleanDiffPasses(t *testing.T) {
	b := gitBox(secretDiff())
	var audits []string
	x := &Executor{Exec: b.exec, Scan: scanWith(scanner.ModeWarn, true), Audit: func(a, d string) { audits = append(audits, a) }}
	if o := run(x, ToolGit, `{"action":"push","branch":"feature"}`); o.IsError {
		t.Fatal("warn mode must allow")
	}
	if len(audits) == 0 || audits[0] != "scan.warn" {
		t.Fatalf("%v", audits)
	}
	b2 := gitBox("+hello world\n")
	x = &Executor{Exec: b2.exec, Scan: scanWith(scanner.ModeBlock, true)}
	if o := run(x, ToolGit, `{"action":"commit","message":"x"}`); o.IsError {
		t.Fatalf("%+v", o)
	}
}

func TestSecretScannerBlocksCommit(t *testing.T) {
	b := gitBox(secretDiff())
	x := &Executor{Exec: b.exec, Scan: scanWith(scanner.ModeBlock, true)}
	o := run(x, ToolGit, `{"action":"commit","message":"add config"}`)
	if !o.Blocked {
		t.Fatal("commit with a planted key was not blocked")
	}
	for _, c := range b.cmds {
		if strings.HasPrefix(c, "git commit") {
			t.Fatal("commit ran")
		}
	}
}

func TestWriteFileWarnsAboutSecrets(t *testing.T) {
	b := newBox()
	x := &Executor{Exec: b.exec, Scan: scanWith(scanner.ModeBlock, true)}
	o := run(x, ToolWrite, `{"path":"cfg.env","content":"TOKEN=ghp_`+strings.Repeat("aB3dE5gH7j", 4)+`"}`)
	if o.IsError || !strings.Contains(o.Text, "looks like it contains a secret") {
		t.Fatalf("%+v", o)
	}
}

func TestLoadSkill(t *testing.T) {
	fs := rules.MemFS{
		"/workspace/.syntaxis/skills/deploy/SKILL.md":  "---\nname: deploy\ndescription: Deploy it\n---\nSteps: run make deploy",
		"/workspace/.syntaxis/skills/deploy/notes.txt": "extra notes",
	}
	l, _ := rules.Load(context.Background(), fs, "/workspace", rules.Options{RepoSkills: true})
	b := newBox()
	b.files["/workspace/.syntaxis/skills/deploy/SKILL.md"] = fs["/workspace/.syntaxis/skills/deploy/SKILL.md"]
	b.files["/workspace/.syntaxis/skills/deploy/notes.txt"] = "extra notes"
	x := &Executor{Exec: b.exec, Rules: l}
	o := run(x, ToolLoadSkill, `{"name":"deploy"}`)
	if o.IsError || !strings.Contains(o.Text, "make deploy") || !o.Untrusted {
		t.Fatalf("%+v", o)
	}
	if o := run(x, ToolLoadSkill, `{"name":"deploy","file":"notes.txt"}`); o.IsError || o.Text != "extra notes" {
		t.Fatalf("%+v", o)
	}
	for _, bad := range []string{`{"name":"deploy","file":"../../../etc/passwd"}`, `{"name":"deploy","file":"/etc/passwd"}`, `{"name":"nope"}`} {
		if o := run(x, ToolLoadSkill, bad); !o.IsError {
			t.Errorf("accepted %s", bad)
		}
	}
	if o := run(&Executor{Exec: b.exec}, ToolLoadSkill, `{"name":"x"}`); !o.IsError {
		t.Fatal("no rules loaded")
	}
}

func TestAskUserAndUnknownTool(t *testing.T) {
	x, _ := newExec()
	if o := run(x, ToolAsk, `{"question":"which?"}`); o.Question != "which?" {
		t.Fatal("ask")
	}
	if o := run(x, ToolAsk, `{}`); !o.IsError {
		t.Fatal("empty ask")
	}
	if o := run(x, "nope", `{}`); !o.IsError {
		t.Fatal("unknown")
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]struct{ name, args, cmd string }{
		"shell":  {ToolShell, `{"command":"ls -la"}`, "ls -la"},
		"push":   {ToolGit, `{"action":"push","branch":"b"}`, "git push origin b"},
		"commit": {ToolGit, `{"action":"commit"}`, "git commit"},
		"pr":     {ToolGit, `{"action":"pr"}`, "gh pr create"},
		"write":  {ToolWrite, `{"path":"a"}`, ""},
	}
	for n, c := range cases {
		if cmd, sum := Describe(c.name, json.RawMessage(c.args)); cmd != c.cmd || sum == "" {
			t.Errorf("%s: %q %q", n, cmd, sum)
		}
	}
}

func TestToolDefsAreValidJSONSchema(t *testing.T) {
	for _, withSkills := range []bool{false, true} {
		defs := ToolDefs(withSkills)
		names := map[string]bool{}
		for _, d := range defs {
			if d.Name == "" || d.Description == "" || !json.Valid(d.Parameters) {
				t.Errorf("bad def %+v", d)
			}
			names[d.Name] = true
		}
		if names[ToolLoadSkill] != withSkills {
			t.Fatalf("skills tool presence wrong (withSkills=%v)", withSkills)
		}
	}
	if !ToolNameSet(true)[ToolGit] {
		t.Fatal("name set")
	}
}

func TestSystemPromptContent(t *testing.T) {
	p := BuildSystemPrompt(PromptOptions{Autonomy: Workspace, ToolsEnabled: true})
	for _, want := range []string{"/workspace", "plan first", "Untrusted content", "approval", "ask_user"} {
		if !strings.Contains(strings.ToLower(p), strings.ToLower(want)) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if !strings.Contains(BuildSystemPrompt(PromptOptions{Autonomy: AskAll, ToolsEnabled: true}), "shown to the user for approval") {
		t.Fatal("ask-all text")
	}
	if !strings.Contains(BuildSystemPrompt(PromptOptions{Autonomy: Full, ToolsEnabled: true}), "fully autonomous") {
		t.Fatal("full text")
	}
	if !strings.Contains(BuildSystemPrompt(PromptOptions{ToolsEnabled: false}), "Tools are not available") {
		t.Fatal("chat only")
	}
	if !strings.Contains(BuildSystemPrompt(PromptOptions{ToolsEnabled: true, CloudToolsOff: true}), "Cloud tooling is disabled") {
		t.Fatal("cloud off")
	}
	if ContextSummaryNote("x") == "" {
		t.Fatal("note")
	}
}

func TestSignedCommitFlowKeepsKeyOutOfSandbox(t *testing.T) {
	b := newBox()
	b.run = func(argv []string) (sandbox.ExecResult, bool) {
		j := strings.Join(argv, " ")
		switch {
		case strings.HasPrefix(j, "git diff --cached"):
			return sandbox.ExecResult{}, true
		case strings.HasPrefix(j, "git cat-file commit"):
			return sandbox.ExecResult{Stdout: "tree abc\nauthor a <a@example.com> 1 +0000\ncommitter a <a@example.com> 1 +0000\n\nmsg\n"}, true
		case strings.HasPrefix(j, "git hash-object"):
			return sandbox.ExecResult{Stdout: "deadbeef\n"}, true
		}
		return sandbox.ExecResult{}, false
	}
	var payloads []string
	x := &Executor{Exec: b.exec, SigningMode: "required", Signer: func(_ context.Context, p string) (string, error) {
		payloads = append(payloads, p)
		return "-----BEGIN SSH SIGNATURE-----\nSIG\n-----END SSH SIGNATURE-----\n", nil
	}}
	if o := run(x, ToolGit, `{"action":"commit","message":"x"}`); o.IsError {
		t.Fatalf("%+v", o)
	}
	joined := strings.Join(b.cmds, "\n")
	for _, want := range []string{"git commit", "git cat-file commit HEAD", "git hash-object -t commit -w --stdin", "git update-ref HEAD deadbeef"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing step %q in:\n%s", want, joined)
		}
	}
	if len(payloads) != 1 || strings.Contains(payloads[0], "gpgsig") {
		t.Fatal("payload")
	}
	if strings.Contains(joined, "PRIVATE KEY") {
		t.Fatal("private key material in a sandbox command")
	}
}

func TestSignedCommitRequiredFailureUndoesCommit(t *testing.T) {
	b := newBox()
	b.run = func(argv []string) (sandbox.ExecResult, bool) {
		if strings.HasPrefix(strings.Join(argv, " "), "git cat-file") {
			return sandbox.ExecResult{Stdout: "tree abc\n\nmsg\n"}, true
		}
		return sandbox.ExecResult{}, false
	}
	x := &Executor{Exec: b.exec, SigningMode: "required", Signer: func(context.Context, string) (string, error) { return "", errors.New("key unavailable") }}
	o := run(x, ToolGit, `{"action":"commit","message":"x"}`)
	if !o.IsError || !strings.Contains(strings.Join(b.cmds, "\n"), "git reset --soft HEAD~1") {
		t.Fatalf("required mode must not leave an unsigned commit: %+v %v", o, b.cmds)
	}
	// required with no signer at all refuses before committing
	b2 := newBox()
	if o := run(&Executor{Exec: b2.exec, SigningMode: "required"}, ToolGit, `{"action":"commit","message":"x"}`); !o.IsError {
		t.Fatal("required without signer allowed")
	}
	for _, c := range b2.cmds {
		if strings.HasPrefix(c, "git commit") {
			t.Fatal("committed")
		}
	}
	// optional mode: sign failure is a note, commit stays
	x2 := &Executor{Exec: b.exec, SigningMode: "optional", Signer: func(context.Context, string) (string, error) { return "", errors.New("nope") }}
	if o := run(x2, ToolGit, `{"action":"commit","message":"x"}`); o.IsError || !strings.Contains(o.Text, "could not be signed") {
		t.Fatalf("%+v", o)
	}
}
