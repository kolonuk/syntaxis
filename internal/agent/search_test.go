// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/scanner"
)

func TestWebSearchResultsAreUntrustedAndBounded(t *testing.T) {
	x, _ := newExec()
	var gotQuery string
	var gotMax int
	x.Search = func(_ context.Context, q string, n int) ([]SearchResult, error) {
		gotQuery, gotMax = q, n
		return []SearchResult{{Title: "Go slices", URL: "https://go.dev/ref", Snippet: "package slices"}}, nil
	}
	o := run(x, ToolWebSearch, `{"query":"  go slices  ","max_results":99}`)
	if o.IsError || !o.Untrusted || !strings.Contains(o.Text, "https://go.dev/ref") || gotQuery != "go slices" || gotMax != 5 {
		t.Fatalf("%+v %q %d", o, gotQuery, gotMax)
	}
	if o := run(x, ToolWebSearch, `{"query":"x","max_results":3}`); gotMax != 3 || o.IsError {
		t.Fatal(gotMax)
	}
	for name, args := range map[string]string{"empty": `{"query":"  "}`, "none": `{}`, "long": `{"query":"` + strings.Repeat("a", 301) + `"}`} {
		if o := run(x, ToolWebSearch, args); !o.IsError {
			t.Errorf("%s accepted", name)
		}
	}
	x.Search = func(context.Context, string, int) ([]SearchResult, error) { return nil, nil }
	if o := run(x, ToolWebSearch, `{"query":"nothing"}`); o.IsError || !strings.Contains(o.Text, "no results") {
		t.Fatalf("%+v", o)
	}
	x.Search = func(context.Context, string, int) ([]SearchResult, error) {
		return nil, errors.New("the search server could not be reached")
	}
	if o := run(x, ToolWebSearch, `{"query":"x"}`); !o.IsError || !strings.Contains(o.Text, "could not be reached") {
		t.Fatalf("%+v", o)
	}
}

// A query leaves for a public search engine, so a secret in it must be stopped before it goes.
func TestWebSearchRefusesQueriesWithSecrets(t *testing.T) {
	x, _ := newExec()
	called := false
	x.Search = func(context.Context, string, int) ([]SearchResult, error) { called = true; return nil, nil }
	x.Scan = func(text string, force bool) scanner.Decision {
		return scanner.Evaluate(text, scanner.ModeBlock, false, force)
	}
	o := run(x, ToolWebSearch, `{"query":"why does AKIAIOSFODNN7EXAMPLE give access denied"}`)
	if !o.IsError || called || !strings.Contains(o.Text, "secret") {
		t.Fatalf("%+v called=%v", o, called)
	}
	if o := run(x, ToolWebSearch, `{"query":"go slices package"}`); o.IsError || !called {
		t.Fatalf("a normal query was refused: %+v", o)
	}
}

func TestWebSearchOnlyOfferedWhenSetUpAndNotWhilePlanning(t *testing.T) {
	x, _ := newExec()
	if o := run(x, ToolWebSearch, `{"query":"x"}`); !o.IsError || !strings.Contains(o.Text, "not set up") {
		t.Fatalf("%+v", o)
	}
	h := newHarness(t)
	h.a.Deps.Tools.Search = func(context.Context, string, int) ([]SearchResult, error) { return nil, nil }
	h.model.replies = []*provider.ChatResult{{Content: "ok"}}
	h.a.Run(context.Background(), "hi")
	if !hasTool(h.model.requests[0].Tools, ToolWebSearch) {
		t.Fatal("not offered although a search server is set up")
	}
	h = newHarness(t)
	h.model.replies = []*provider.ChatResult{{Content: "ok"}}
	h.a.Run(context.Background(), "hi")
	if hasTool(h.model.requests[0].Tools, ToolWebSearch) {
		t.Fatal("offered with no search server")
	}
	h = newHarness(t)
	h.a.Cfg.PlanPhase = true
	h.a.Deps.Tools.Search = func(context.Context, string, int) ([]SearchResult, error) { return nil, nil }
	h.model.replies = []*provider.ChatResult{{Content: "ok"}}
	h.a.Run(context.Background(), "hi")
	if hasTool(h.model.requests[0].Tools, ToolWebSearch) {
		t.Fatal("offered while planning")
	}
}

func hasTool(defs []provider.ToolDef, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}
