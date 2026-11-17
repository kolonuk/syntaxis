// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

const goFile = "package main\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n\nfunc sub(a, b int) int {\n\treturn a - b\n}\n"

func TestPatchToleratesWrongIndentation(t *testing.T) {
	x, b := newExec()
	b.files["/workspace/m.go"] = goFile
	// the model used spaces where the file has a tab
	o := run(x, ToolPatch, `{"path":"m.go","old":"func add(a, b int) int {\n    return a + b\n}","new":"func add(a, b int) int {\n    return a + b + 0\n}"}`)
	if o.IsError {
		t.Fatal(o.Text)
	}
	got := b.files["/workspace/m.go"]
	if !strings.Contains(got, "\treturn a + b + 0\n}") || strings.Contains(got, "    return") {
		t.Fatalf("not re-indented to the file's own style:\n%s", got)
	}
	if !strings.Contains(o.Text, "ignoring differences") {
		t.Fatalf("the model is not told the match was loose: %q", o.Text)
	}
	if !strings.Contains(got, "func sub") || !strings.HasSuffix(got, "}\n") {
		t.Fatalf("the rest of the file changed:\n%s", got)
	}
}

func TestPatchDoesNotGuessWhenLooseMatchIsAmbiguous(t *testing.T) {
	x, b := newExec()
	orig := "if a {\n\tx()\n}\nif b {\n\tx()\n}\n"
	b.files["/workspace/f.go"] = orig
	o := run(x, ToolPatch, `{"path":"f.go","old":"  x()","new":"y()"}`)
	if !o.IsError || !strings.Contains(o.Text, "matches 2 places") {
		t.Fatalf("%+v", o)
	}
	if b.files["/workspace/f.go"] != orig {
		t.Fatal("an ambiguous edit changed the file")
	}
}

func TestPatchMissShowsNearestText(t *testing.T) {
	x, b := newExec()
	b.files["/workspace/m.go"] = goFile
	o := run(x, ToolPatch, `{"path":"m.go","old":"func sub(a, b int) int {\n\treturn a - c\n}","new":"x"}`)
	if !o.IsError || !strings.Contains(o.Text, "closest text is at line 7") || !strings.Contains(o.Text, "7\tfunc sub(a, b int) int {") {
		t.Fatalf("no nearest match shown: %q", o.Text)
	}
	o = run(x, ToolPatch, `{"path":"m.go","old":"completely unrelated words here","new":"x"}`)
	if !o.IsError || strings.Contains(o.Text, "closest") || !strings.Contains(o.Text, "not found") {
		t.Fatalf("a far-off block was presented as close: %q", o.Text)
	}
}

func TestApplyPatchEditsAreAllOrNothing(t *testing.T) {
	x, b := newExec()
	b.files["/workspace/m.go"] = goFile
	o := run(x, ToolApply, `{"path":"m.go","edits":[{"old":"return a + b","new":"return b + a"},{"old":"return a - b","new":"return b - a"}]}`)
	if o.IsError || !strings.Contains(o.Text, "2 edits") {
		t.Fatal(o.Text)
	}
	if got := b.files["/workspace/m.go"]; !strings.Contains(got, "return b + a") || !strings.Contains(got, "return b - a") {
		t.Fatalf("not applied:\n%s", got)
	}
	before := b.files["/workspace/m.go"]
	o = run(x, ToolApply, `{"path":"m.go","edits":[{"old":"return b + a","new":"return 0"},{"old":"does not exist at all","new":"y"}]}`)
	if !o.IsError || !strings.Contains(o.Text, "edit 2 of 2") || !strings.Contains(o.Text, "No edit was applied") {
		t.Fatalf("%q", o.Text)
	}
	if b.files["/workspace/m.go"] != before {
		t.Fatal("the first edit was applied although the second failed")
	}
}

func TestApplyPatchEditsSeeEarlierEdits(t *testing.T) {
	x, b := newExec()
	b.files["/workspace/a.txt"] = "one\ntwo\n"
	if o := run(x, ToolApply, `{"path":"a.txt","edits":[{"old":"one","new":"uno"},{"old":"uno\ntwo","new":"uno\ndos"}]}`); o.IsError {
		t.Fatal(o.Text)
	}
	if b.files["/workspace/a.txt"] != "uno\ndos\n" {
		t.Fatalf("%q", b.files["/workspace/a.txt"])
	}
}

func TestApplyPatchUnifiedDiffIgnoresLineNumbers(t *testing.T) {
	x, b := newExec()
	b.files["/workspace/m.go"] = goFile
	diff := "--- a/m.go\n+++ b/m.go\n@@ -99,3 +99,3 @@\n func add(a, b int) int {\n-\treturn a + b\n+\treturn a + b + 1\n }\n@@ -1,2 +1,2 @@\n-package main\n+package calc\n \n"
	o := run(x, ToolApply, mustJSON(map[string]any{"diff": diff}))
	if o.IsError {
		t.Fatal(o.Text)
	}
	got := b.files["/workspace/m.go"]
	if !strings.HasPrefix(got, "package calc\n") || !strings.Contains(got, "return a + b + 1") || !strings.Contains(got, "return a - b") {
		t.Fatalf("diff not applied by content:\n%s", got)
	}
}

func TestApplyPatchArgumentChecks(t *testing.T) {
	x, b := newExec()
	b.files["/workspace/a.txt"] = "x\n"
	for name, args := range map[string]string{
		"neither":      `{"path":"a.txt"}`,
		"both":         `{"path":"a.txt","edits":[{"old":"x","new":"y"}],"diff":"@@\n-x\n+y\n"}`,
		"no hunks":     `{"path":"a.txt","diff":"just text"}`,
		"no path":      `{"edits":[{"old":"x","new":"y"}]}`,
		"empty old":    `{"path":"a.txt","edits":[{"old":"","new":"y"}]}`,
		"escapes":      `{"path":"/etc/passwd","edits":[{"old":"x","new":"y"}]}`,
		"missing file": `{"path":"nope.txt","edits":[{"old":"x","new":"y"}]}`,
	} {
		if o := run(x, ToolApply, args); !o.IsError {
			t.Errorf("%s: accepted: %q", name, o.Text)
		}
	}
	if b.files["/workspace/a.txt"] != "x\n" {
		t.Fatal("file changed by a rejected call")
	}
}

func TestApplyPatchIsBlockedWhilePlanning(t *testing.T) {
	x, b := newExec()
	x.PlanOnly = true
	b.files["/workspace/a.txt"] = "x\n"
	if o := run(x, ToolApply, `{"path":"a.txt","edits":[{"old":"x","new":"y"}]}`); !o.IsError || b.files["/workspace/a.txt"] != "x\n" {
		t.Fatalf("a plan-only run changed a file: %+v", o)
	}
	for _, d := range PlanToolDefs(false) {
		if d.Name == ToolApply || d.Name == ToolPatch {
			t.Fatalf("%s offered while planning", d.Name)
		}
	}
}

func TestSimilarity(t *testing.T) {
	if similarity("  a  b", "a b") != 1 {
		t.Fatal("whitespace should not matter")
	}
	if s := similarity("return a - b", "return a - c"); s < 0.6 || s >= 1 {
		t.Fatal(s)
	}
	if similarity("abc", "xyz") != 0 {
		t.Fatal("unrelated lines")
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
