// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

type stubProvider struct {
	mu      sync.Mutex
	replies []*provider.ChatResult
	reqs    []provider.ChatRequest
}

func (s *stubProvider) Kind() string                                             { return "ollama" }
func (s *stubProvider) Ping(context.Context) (time.Duration, error)              { return time.Millisecond, nil }
func (s *stubProvider) ListModels(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (s *stubProvider) ModelDetail(context.Context, string) (*provider.ModelDetail, error) {
	return &provider.ModelDetail{}, nil
}
func (s *stubProvider) Loaded(context.Context) ([]provider.LoadedModel, bool, error) {
	return nil, false, nil
}
func (s *stubProvider) Chat(_ context.Context, req provider.ChatRequest, on func(string)) (*provider.ChatResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	i := len(s.reqs) - 1
	r := &provider.ChatResult{Content: "done", Usage: provider.Usage{PromptTokens: 50, CompletionTokens: 10, Reported: true}}
	if i < len(s.replies) {
		r = s.replies[i]
	}
	if on != nil && r.Content != "" {
		on(r.Content)
	}
	return r, nil
}

func TestEndToEndAgentRunThroughServer(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fake := sandbox.NewFake()
	fake.ExecHook = func(c *sandbox.FakeContainer, ex sandbox.ExecSpec) (sandbox.ExecResult, bool) {
		j := strings.Join(ex.Cmd, " ")
		if strings.Contains(j, "go test") {
			return sandbox.ExecResult{Stdout: "ok  \tpkg\t0.01s"}, true
		}
		if ex.Cmd[0] == "git" || strings.Contains(j, "head -c") || strings.Contains(j, "[ -d") {
			return sandbox.ExecResult{ExitCode: 1}, true
		}
		return sandbox.ExecResult{}, false
	}
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	call := provider.ToolCall{ID: "c1", Name: "run_shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)}
	sp := &stubProvider{replies: []*provider.ChatResult{
		{ToolCalls: []provider.ToolCall{call}, Usage: provider.Usage{PromptTokens: 100, CompletionTokens: 20, Reported: true}},
		{Content: "Tests pass.", Usage: provider.Usage{PromptTokens: 150, CompletionTokens: 5, Reported: true}},
	}}
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return sp, nil }
	var notified []string
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{Notify: func(_ context.Context, uid int64, title, body string) { notified = append(notified, title) }})

	// a user secret that must reach the sandbox env but never the model
	u := e.userSession("a@example.com")
	u.do("PUT", "/api/secrets/DEPLOY_TOKEN", map[string]string{"value": "tok-very-secret-123"})
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	if code, _, raw := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "run the tests"}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	e.app.Runs.Get(id).Wait()

	// sandbox created under gVisor with that user's volume and the injected secret
	if len(fake.Containers) != 1 {
		t.Fatalf("containers: %d", len(fake.Containers))
	}
	for _, c := range fake.Containers {
		if c.Spec.Runtime != "runsc" || c.Spec.Mounts[0].Volume != "syntaxis-ws-u1" {
			t.Fatalf("%+v", c.Spec)
		}
		if strings.Contains(strings.Join(c.Spec.Env, " "), "tok-very-secret") {
			t.Fatal("secrets must not be baked into the container: they are supplied per command so changes apply at once")
		}
		seen := false
		for _, ex := range c.Execs {
			if strings.Contains(strings.Join(ex.Env, " "), "DEPLOY_TOKEN=tok-very-secret-123") {
				seen = true
			}
		}
		if !seen {
			t.Fatal("the user's secret did not reach the agent's command")
		}
	}
	// history persisted, tool result included
	_, _, raw := u.do("GET", "/api/sessions/"+id+"/messages", nil)
	for _, want := range []string{"run the tests", "go test ./...", "ok  ", "Tests pass."} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("history missing %q: %s", want, raw)
		}
	}
	// the secret never reached the model
	for _, req := range sp.reqs {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "tok-very-secret") {
				t.Fatal("secret in model input")
			}
		}
	}
	// usage recorded against the user's quota; session idle again; notified
	_, usage, _ := u.do("GET", "/api/usage", nil)
	d := usage["daily"].(map[string]any)
	if d["Used"] != float64(100+20+150+5) {
		t.Fatalf("usage %v", d)
	}
	_, got, _ := u.do("GET", "/api/sessions/"+id, nil)
	if got["state"] != "idle" || got["tokens_in"] != float64(250) {
		t.Fatalf("%v", got)
	}
	if len(notified) != 1 || notified[0] != "Your session finished" {
		t.Fatalf("%v", notified)
	}
	// follow-up message resumes with the stored history and the same sandbox
	sp.replies = nil
	u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "thanks"})
	e.app.Runs.Get(id).Wait()
	last := sp.reqs[len(sp.reqs)-1].Messages
	joined := ""
	for _, m := range last {
		joined += m.Content + "|"
	}
	if !strings.Contains(joined, "run the tests") || len(fake.Containers) != 1 {
		t.Fatalf("resume lost history or created a second sandbox: %s / %d", joined, len(fake.Containers))
	}
}

func TestRunRefusedWithoutGVisor(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fake := sandbox.NewFake()
	fake.Runtimes["runsc"] = false
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return &stubProvider{}, nil }
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "hi"})
	e.app.Runs.Get(id).Wait()
	_, got, _ := u.do("GET", "/api/sessions/"+id, nil)
	if got["state"] != "error" || len(fake.Containers) != 0 {
		t.Fatalf("an unisolated container may never be created: %v / %d", got, len(fake.Containers))
	}
}

func TestQuotaExhaustionStopsRunBeforeModelCall(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = sandbox.NewManager(sandbox.NewFake(), sandbox.DefaultConfig(), nil)
	sp := &stubProvider{}
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return sp, nil }
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	u := e.userSession("a@example.com")
	users, _ := e.st.ListUsers(context.Background())
	_ = e.st.AddUsage(context.Background(), users[0].ID, "2026-11-01", 100000, 0, 0)
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "hi"})
	e.app.Runs.Get(id).Wait()
	if len(sp.reqs) != 0 {
		t.Fatal("model was called although the quota was exhausted")
	}
	_, _, raw := u.do("GET", "/api/sessions/"+id+"/events", nil)
	if !strings.Contains(string(raw), "quota") {
		t.Fatalf("user not told why: %s", raw)
	}
}

func TestPreviewProxyOwnerOnlyAndStripsCookies(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	var gotCookie, gotPath string
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie, gotPath = r.Header.Get("Cookie"), r.URL.Path
		http.SetCookie(w, &http.Cookie{Name: "evil", Value: "x"})
		w.Write([]byte("hello from dev server"))
	}))
	defer dev.Close()
	_, portStr, _ := net.SplitHostPort(dev.Listener.Addr().String())
	fake := sandbox.NewFake()
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	alice := e.userSession("alice@example.com")
	bob := e.userSession("bob@example.com")
	_, s, _ := alice.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	cid, _ := e.app.Sandbox.Create(context.Background(), 1, id, nil, false)
	fake.IPs = map[string]string{cid: "127.0.0.1"}
	sess, _ := e.st.GetChatSession(context.Background(), id)
	sess.SandboxID = cid
	_ = e.st.UpdateChatSession(context.Background(), *sess)

	code, _, raw := alice.do("GET", "/preview/"+id+"/"+portStr+"/app/index.html", nil)
	if code != 200 || string(raw) != "hello from dev server" || gotPath != "/app/index.html" {
		t.Fatalf("%d %q path=%q", code, raw, gotPath)
	}
	if strings.Contains(gotCookie, "syx_") {
		t.Fatalf("Syntaxis session cookie forwarded to user code: %q", gotCookie)
	}
	if code, _, _ := bob.do("GET", "/preview/"+id+"/"+portStr+"/", nil); code != 404 {
		t.Fatalf("another user reached the preview: %d", code)
	}
	if code, _, _ := alice.do("GET", "/preview/"+id+"/80/", nil); code != 400 {
		t.Fatalf("privileged port allowed: %d", code)
	}
	anon := e.client(e.user.URL)
	if code, _, _ := anon.do("GET", "/preview/"+id+"/"+portStr+"/", nil); code != 401 {
		t.Fatalf("unauthenticated preview: %d", code)
	}
	req, _ := http.NewRequest("GET", e.user.URL+"/preview/"+id+"/"+portStr+"/", nil)
	for _, ck := range alice.hc.Jar.Cookies(mustURL(e.user.URL)) {
		req.AddCookie(ck)
	}
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.Header.Get("Set-Cookie") != "" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("previewed content must be sandboxed and unable to set cookies on our origin")
	}
}

func TestBenchmarkAdminOnlyAndStoresResult(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fake := sandbox.NewFake()
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	sp := &stubProvider{} // answers "done" to everything: every task fails, which is a valid, stored result
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return sp, nil }

	usr := e.userSession("a@example.com")
	for _, p := range []string{"/api/models/1/benchmark", "/api/benchmarks", "/api/models/1/recommended"} {
		if code, _, _ := usr.do("POST", p, map[string]bool{"recommended": true}); code == 200 || code == 202 {
			t.Fatalf("chat user reached admin benchmark route %s: %d", p, code)
		}
	}
	adm := e.makeAdminSession()
	if code, _, raw := adm.do("POST", "/api/models/1/benchmark", nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	var list []map[string]any
	for i := 0; i < 100; i++ {
		_, _, raw := adm.do("GET", "/api/benchmarks", nil)
		_ = json.Unmarshal(raw, &list)
		if len(list) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(list) != 1 || list[0]["report"] == nil {
		t.Fatalf("no benchmark stored: %v", list)
	}
	if code, _, _ := adm.do("POST", "/api/models/1/recommended", map[string]bool{"recommended": true}); code != 200 {
		t.Fatal("recommend")
	}
	// the "recommended" flag is visible to users; the report is not
	_, _, raw := usr.do("GET", "/api/models", nil)
	if !strings.Contains(string(raw), `"recommended":true`) || strings.Contains(string(raw), "pass_rate") {
		t.Fatalf("%s", raw)
	}
	for i := 0; i < 100 && fake.Count() != 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if fake.Count() != 0 {
		t.Fatal("throwaway benchmark sandbox was not destroyed")
	}
}

func TestMergeEnvOwnEntriesWinAndOrderIsStable(t *testing.T) {
	got := mergeEnv([]string{"A=own", "TERM=x"}, map[string]string{"A": "secret", "B": "2", "C": "3"})
	if strings.Join(got, " ") != "A=own TERM=x B=2 C=3" {
		t.Fatalf("%v", got)
	}
}
