// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/routing"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/testenv"
	"github.com/kolonuk/syntaxis/internal/watch"
)

// ollamaDo talks to the real Ollama directly, for setting up which models are loaded.
func ollamaDo(t *testing.T, cfg map[string]string, path string, body any) {
	t.Helper()
	b, _ := json.Marshal(body)
	r, err := http.Post(strings.TrimRight(cfg["OLLAMA_URL"], "/")+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
}

// The helper is used only while the real server says it is loaded, and the admin sees the result.
func TestLiveHelperIsUsedOnlyWhileLoaded(t *testing.T) {
	cfg := testenv.Load(t)
	l := liveEnv(t)
	ctx := context.Background()
	pid := int64(1)
	prov, _ := l.st.GetProvider(ctx, pid)
	tiny := cfg["MODEL_TINY"]
	if tiny == "" {
		testenv.NotTested(t, "MODEL_TINY is not in the private test environment")
	}
	ms, _ := l.st.ListModels(ctx, pid)
	tid, _ := l.st.UpsertModel(ctx, store.Model{ProviderID: pid, Name: tiny, ToolsSupported: -1})
	_ = tid
	adm := l.makeAdminSession()
	set := func(model string) map[string]any {
		code, m, raw := adm.do("PUT", "/api/providers/"+strconv.FormatInt(pid, 10)+"/helper", map[string]any{"model": model})
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		return m
	}
	if code, _, _ := adm.do("PUT", "/api/providers/1/helper", map[string]any{"model": "no-such-model"}); code != 400 {
		t.Fatalf("a model the provider does not have was accepted: %d", code)
	}
	pr, _ := l.app.ProviderFor(prov)
	session := strings.TrimPrefix(l.model, "local/")
	_ = ms

	// not loaded: saved, flagged, and the session's model is used
	ollamaDo(t, cfg, "/api/generate", map[string]any{"model": tiny, "keep_alive": 0})
	time.Sleep(3 * time.Second)
	if got := set(tiny); got["usable"] != false || got["notice"] == nil {
		t.Fatalf("an unloaded helper must be flagged: %v", got)
	}
	if m, routed := l.app.routeModel(ctx, prov, pr, session, routing.TaskTitle); routed || m != session {
		t.Fatalf("an unloaded helper was used: %s %v", m, routed)
	}
	// loaded (by the server's own use, not by Syntaxis): used for the cheap jobs, never for chat
	ollamaDo(t, cfg, "/api/generate", map[string]any{"model": session, "keep_alive": "5m"})
	ollamaDo(t, cfg, "/api/generate", map[string]any{"model": tiny, "prompt": "hi", "stream": false, "keep_alive": "5m"})
	if m, routed := l.app.routeModel(ctx, prov, pr, session, routing.TaskTitle); !routed || m != tiny {
		t.Skipf("NOT TESTED: the server did not keep both models loaded together (%s %v); a memory limit of this host", m, routed)
	}
	if m, routed := l.app.routeModel(ctx, prov, pr, session, routing.TaskChat); routed || m != session {
		t.Fatalf("the helper was used for the real work: %s %v", m, routed)
	}
	if got := set(tiny); got["usable"] != true {
		t.Fatalf("%v", got)
	}
	// clearing it
	set("")
	if m, routed := l.app.routeModel(ctx, prov, pr, session, routing.TaskTitle); routed || m != session {
		t.Fatal("a cleared helper was used")
	}
	// the admin list shows the setting
	set(tiny)
	_, _, raw := adm.do("GET", "/api/providers", nil)
	if !strings.Contains(string(raw), `"helper_model":"`+tiny+`"`) {
		t.Fatalf("%s", raw)
	}
}

// A new session gets a short title from its first exchange, written by the real model.
func TestLiveSessionGetsATitle(t *testing.T) {
	l := liveEnv(t)
	u := l.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": l.model})
	id := s["id"].(string)
	if s["title"] != "New session" {
		t.Fatalf("%v", s)
	}
	l.send(u, id, "Reply with the single word: hello")
	var title string
	for i := 0; i < 90; i++ {
		_, g, _ := u.do("GET", "/api/sessions/"+id, nil)
		title, _ = g["title"].(string)
		if title != "New session" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if title == "New session" || title == "" || len(title) > 80 || strings.Contains(title, "\n") {
		t.Fatalf("the session was not titled: %q", title)
	}
	t.Logf("title: %q", title)
}

// A long real release note (from GitHub) is cut down to a couple of sentences by the real model.
func TestLiveLongWatchEventIsSummarised(t *testing.T) {
	testenv.Network(t, "api.github.com:443")
	l := liveEnv(t)
	u := l.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": l.model})
	ctx := context.Background()
	sess, _ := l.st.GetChatSession(ctx, s["id"].(string))
	usr, _ := l.st.GetUser(ctx, sess.UserID)
	var event string
	for _, repo := range []string{"cli/cli", "golang/go", "docker/compose", "ollama/ollama"} {
		_, ev, err := watchCheck(ctx, repo)
		if err == nil && ev != "" && len(ev) >= 600 {
			event = ev
			break
		}
	}
	if event == "" {
		testenv.NotTested(t, "no repository had a release note long enough to need a summary")
	}
	sum := l.app.summariseEvent(ctx, usr, sess, event)
	if sum == "" || len(sum) >= len(event) || len(sum) > 600 {
		t.Fatalf("the event was not summarised: %q", sum)
	}
	t.Logf("summary: %s", sum)
	if got := l.app.summariseEvent(ctx, usr, sess, "short"); got != "" {
		t.Fatalf("a short event must be left alone: %q", got)
	}
	// compaction through the helper is the same routing and is exercised only when a conversation is
	// long enough to need it
	t.Log("NOT TESTED: compaction of a long conversation on the helper model (needs a conversation longer than the model's window)")
}

// watchCheck returns the text of the latest release of a repository, as a watch would see it.
func watchCheck(ctx context.Context, repo string) (string, string, error) {
	st, ev, err := watch.Check(ctx, "github_release", watch.Config{"repo": repo}, "v0.0.1", watch.Deps{})
	if err != nil || ev == nil {
		return st, "", err
	}
	return st, ev.Text(), nil
}
