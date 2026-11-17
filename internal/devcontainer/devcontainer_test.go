// SPDX-License-Identifier: GPL-2.0-or-later

package devcontainer

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/testenv"
)

var templates = []Template{
	{Name: "node", Image: "syntaxis-sandbox:latest", Matches: []string{"mcr.microsoft.com/devcontainers/javascript-node", "node"}},
	{Name: "python", Image: "syntaxis-python:1", Matches: []string{"mcr.microsoft.com/devcontainers/python"}},
}

func TestParseAcceptsCommentsAndTrailingCommas(t *testing.T) {
	c, err := Parse([]byte(`{
		// a comment with "quotes" and a // inside
		"name": "x // not a comment",
		"image": "mcr.microsoft.com/devcontainers/python:3-3.14", /* block */
		"containerEnv": {"A": "1", "bad name": "x", "B": "two\nlines"},
		"postCreateCommand": ["pip", "install", "-r", "it's.txt",],
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "x // not a comment" || c.Image != "mcr.microsoft.com/devcontainers/python:3-3.14" {
		t.Fatalf("%+v", c)
	}
	if len(c.ContainerEnv) != 1 || c.ContainerEnv["A"] != "1" {
		t.Fatalf("only well-formed variables are kept: %v", c.ContainerEnv)
	}
	if c.PostCreate != `'pip' 'install' '-r' 'it'\''s.txt'` {
		t.Fatalf("the arguments must be quoted: %s", c.PostCreate)
	}
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestPostCreateForms(t *testing.T) {
	c, _ := Parse([]byte(`{"postCreateCommand": "npm install"}`))
	if c.PostCreate != "npm install" {
		t.Fatal(c.PostCreate)
	}
	c, _ = Parse([]byte(`{"postCreateCommand": {"b": "echo two", "a": ["echo", "one"]}}`))
	if c.PostCreate != "('echo' 'one') && (echo two)" {
		t.Fatalf("named commands run in name order: %s", c.PostCreate)
	}
}

func TestChooseOnlyApprovedEnvironments(t *testing.T) {
	for _, tc := range []struct {
		json, want, why string
	}{
		{`{"image":"mcr.microsoft.com/devcontainers/javascript-node:5-26-trixie"}`, "node", ""},
		{`{"image":"mcr.microsoft.com/devcontainers/python"}`, "python", ""},
		{`{"image":"node:22"}`, "node", ""},
		{`{"image":"evil.example.net/devcontainers/python:3"}`, "", "not one of the approved"},
		{`{"image":"mcr.microsoft.com/devcontainers/pythonic:3"}`, "", "not one of the approved"},
		{`{"build":{"dockerfile":"Dockerfile"}}`, "", "Dockerfile"},
		{`{"customizations":{"syntaxis":{"template":"python"}}}`, "python", ""},
		{`{"customizations":{"syntaxis":{"template":"cobol"}}}`, "", "not available"},
		{`{}`, "", ""},
	} {
		c, err := Parse([]byte(tc.json))
		if err != nil {
			t.Fatal(err)
		}
		got, reason, ok := Choose(c, templates)
		if ok != (tc.want != "") || got.Name != tc.want || !strings.Contains(reason, tc.why) {
			t.Errorf("%s: got %q ok=%v reason=%q", tc.json, got.Name, ok, reason)
		}
	}
}

// Real devcontainer.json files from Microsoft's sample repositories: comments, tabs, and options
// that are ignored.
func TestRealFilesFromGitHub(t *testing.T) {
	testenv.Network(t, "raw.githubusercontent.com:443")
	for repo, wantCmd := range map[string]string{"vscode-remote-try-node": "npm install", "vscode-remote-try-python": "pip3 install -r requirements.txt", "vscode-remote-try-go": ""} {
		r, err := http.Get("https://raw.githubusercontent.com/microsoft/" + repo + "/main/.devcontainer/devcontainer.json")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d", repo, r.StatusCode)
		}
		c, err := Parse(b)
		if err != nil || c.Image == "" || c.PostCreate != wantCmd {
			t.Fatalf("%s: %+v %v", repo, c, err)
		}
	}
}
