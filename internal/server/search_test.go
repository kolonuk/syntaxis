// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestParseSearxKeepsOnlyWebResults(t *testing.T) {
	body := []byte(`{"query":"go","number_of_results":0,"results":[
		{"url":"https://go.dev/doc/","title":"Documentation   -  The Go Programming Language","content":"Get\n started with Go","engine":"duckduckgo"},
		{"url":"javascript:alert(1)","title":"bad","content":"x"},
		{"url":"file:///etc/passwd","title":"bad","content":"x"},
		{"url":"http://example.com/a","title":"second","content":""},
		{"url":"https://example.com/b","title":"third","content":"x"}],"suggestions":[]}`)
	rs, err := parseSearx(body, 2)
	if err != nil || len(rs) != 2 {
		t.Fatalf("%v %+v", err, rs)
	}
	if rs[0].Title != "Documentation - The Go Programming Language" || rs[0].Snippet != "Get started with Go" || rs[1].URL != "http://example.com/a" {
		t.Fatalf("%+v", rs)
	}
	if _, err := parseSearx([]byte("<html>forbidden</html>"), 5); err == nil || !strings.Contains(err.Error(), "json format") {
		t.Fatalf("%v", err)
	}
}

func TestSearchURLSetting(t *testing.T) {
	for _, ok := range []string{"", "https://search.example.com", "http://searxng.internal:8080"} {
		if !validSetting(SettingSearchURL, ok) {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"search.example.com", "ftp://x.example.com", "https://user:pw@x.example.com", "https://x.example.com/?q=1", "javascript:alert(1)"} {
		if validSetting(SettingSearchURL, bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSearchIsNotSetUpUntilAnAdminNamesAServer(t *testing.T) {
	e := newEnv(t)
	if e.app.searchFunc(context.Background()) != nil {
		t.Fatal("search is on without a search server")
	}
	_ = e.st.SetSetting(context.Background(), SettingSearchURL, "https://search.example.com")
	if e.app.searchFunc(context.Background()) == nil {
		t.Fatal("search is off although a server is set")
	}
}

// Against a real SearXNG: set SYNTAXIS_TEST_SEARXNG_URL (the json format must be enabled on it).
func TestLiveSearXNG(t *testing.T) {
	url := os.Getenv("SYNTAXIS_TEST_SEARXNG_URL")
	if url == "" {
		t.Skip("NOT TESTED: set SYNTAXIS_TEST_SEARXNG_URL to a SearXNG server with the json format enabled")
	}
	e := newEnv(t)
	e.app.Cfg.ConnectionsAllowPrivate = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8"}
	_ = e.st.SetSetting(context.Background(), SettingSearchURL, url)
	search := e.app.searchFunc(context.Background())
	rs, err := search(context.Background(), "golang programming language", 5)
	if err != nil || len(rs) == 0 {
		t.Fatalf("%v %v", rs, err)
	}
	for _, r := range rs {
		if !strings.HasPrefix(r.URL, "http") || r.Title == "" {
			t.Fatalf("%+v", r)
		}
	}
}
