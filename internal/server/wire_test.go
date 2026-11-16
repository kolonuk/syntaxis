// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

// The whole path with the real model and a real gVisor sandbox: the HTTP API, a background run,
// commands executed by the server in the user's own sandbox, results persisted, usage recorded.
func TestLiveAgentRunEndToEnd(t *testing.T) {
	l := liveEnv(t)
	u := l.userSession("a@example.com")
	// a user secret that must reach the sandbox's commands but never the model
	u.do("PUT", "/api/secrets/DEPLOY_TOKEN", map[string]string{"value": "tok-very-secret-123"})
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": l.model})
	id := s["id"].(string)
	l.send(u, id, "Use the run_shell tool to run exactly this command: echo built-here > /workspace/out.txt && test -n \"$DEPLOY_TOKEN\" && echo has-token > /workspace/token-seen.txt. Then reply with the single word done.")

	if got := l.sandboxRun(id, "cat /workspace/out.txt; cat /workspace/token-seen.txt"); got != "built-here\nhas-token" {
		t.Fatalf("the command did not run as asked in the sandbox (the real model may have chosen differently): %q\n%s", got, l.history(u, id))
	}
	// the sandbox is real gVisor with the user's own volume, and the secret is not baked into it
	sess, _ := l.st.GetChatSession(context.Background(), id)
	ci, err := l.app.Sandbox.RT.Inspect(context.Background(), sess.SandboxID)
	if err != nil || !ci.Running {
		t.Fatalf("%+v %v", ci, err)
	}
	if out := dockerInspect(t, sess.SandboxID, "{{.HostConfig.Runtime}} {{json .Config.Env}}"); !strings.HasPrefix(out, "runsc ") || strings.Contains(out, "tok-very-secret") {
		t.Fatalf("the secret must be supplied per command, not baked into the container: %s", out)
	}
	// the secret never reached the model or the stored history
	if h := l.history(u, id); strings.Contains(h, "tok-very-secret") {
		t.Fatalf("the secret is in the conversation: %s", h)
	}
	// usage recorded against the user, session idle again, user notified
	_, usage, _ := u.do("GET", "/api/usage", nil)
	if d := usage["daily"].(map[string]any); d["Used"].(float64) <= 0 {
		t.Fatalf("usage %v", d)
	}
	_, got, _ := u.do("GET", "/api/sessions/"+id, nil)
	if got["state"] != "idle" || got["tokens_in"].(float64) <= 0 {
		t.Fatalf("%v", got)
	}
	l.mu.Lock()
	n := len(l.notified)
	l.mu.Unlock()
	if n != 1 {
		t.Fatalf("notified %d times", n)
	}
	// a follow-up resumes with the stored history in the same sandbox
	l.send(u, id, "Without running anything, what file did you create in my first message? Answer with its name only.")
	if h := l.history(u, id); !strings.Contains(h, "out.txt") {
		t.Fatalf("the follow-up lost the history: %s", h)
	}
	after, _ := l.st.GetChatSession(context.Background(), id)
	if after.SandboxID != sess.SandboxID {
		t.Fatal("the follow-up created a second sandbox")
	}
}

// A real Docker with the runtime missing: the run must fail, say why, and create nothing.
func TestRunRefusedWithoutGVisor(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	m := realSandbox(t, e)
	m.Cfg.Runtime = "syntaxis-no-such-runtime"
	e.app.Sandbox = m
	cleanupSandboxes(t, e)
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "hi"})
	e.app.Runs.Get(id).Wait()
	_, got, _ := u.do("GET", "/api/sessions/"+id, nil)
	_, _, raw := u.do("GET", "/api/sessions/"+id+"/events", nil)
	if got["state"] != "error" || !strings.Contains(strings.ToLower(string(raw)), "gvisor") {
		t.Fatalf("an unisolated container may never be created, and the user must be told: %v %s", got, raw)
	}
	cs, _ := m.RT.List(context.Background())
	for _, c := range cs {
		if c.Labels[sandbox.LabelSession] == id {
			t.Fatal("a container was created without isolation")
		}
	}
}

// With the quota used up the run stops before any model is contacted. The provider here is a real
// address nothing listens on: if the run tried it, the user would be told the model is unreachable
// instead of being told about the quota.
func TestQuotaExhaustionStopsRunBeforeModelCall(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	u := e.userSession("a@example.com")
	users, _ := e.st.ListUsers(context.Background())
	_ = e.st.AddUsage(context.Background(), users[0].ID, "2026-11-01", 100000, 0, 0)
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "hi"})
	e.app.Runs.Get(id).Wait()
	_, _, raw := u.do("GET", "/api/sessions/"+id+"/events", nil)
	if !strings.Contains(string(raw), "quota") || strings.Contains(strings.ToLower(string(raw)), "unreachable") {
		t.Fatalf("user not told about the quota, or the model was tried first: %s", raw)
	}
}

func dockerInspect(t *testing.T, id, format string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", format, id).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// A real web server runs inside a real sandbox and is reached through the preview proxy.
const previewServer = `import http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Set-Cookie", "evil=x")
        self.send_header("Clear-Site-Data", '"cookies", "storage"')
        self.send_header("Service-Worker-Allowed", "/")
        self.send_header("X-Seen-Cookie", self.headers.get("Cookie", ""))
        self.send_header("X-Seen-Path", self.path)
        self.end_headers()
        self.wfile.write(b"hello from dev server")
    def log_message(self, *a): pass
http.server.HTTPServer(("0.0.0.0", 8000), H).serve_forever()
`

func TestPreviewProxyOwnerOnlyAndStripsCookies(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
	alice := e.userSession("alice@example.com")
	bob := e.userSession("bob@example.com")
	_, s, _ := alice.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	ctx := context.Background()
	sess, _ := e.st.GetChatSession(ctx, id)
	usr, _ := e.st.GetUser(ctx, sess.UserID)
	cid, err := e.app.ensureSandbox(ctx, sess, usr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Sandbox.Exec(ctx, id, cid, sandbox.ExecSpec{Cmd: []string{"sh", "-c", "cat > /tmp/dev.py; (python3 /tmp/dev.py >/dev/null 2>&1 &) ; sleep 1"}, Stdin: []byte(previewServer), Timeout: 20 * time.Second}); err != nil {
		t.Fatal(err)
	}
	var code int
	var raw []byte
	for i := 0; i < 20; i++ { // the server takes a moment to start
		code, _, raw = alice.do("GET", "/preview/"+id+"/8000/app/index.html", nil)
		if code == 200 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if code != 200 || string(raw) != "hello from dev server" {
		t.Fatalf("%d %q", code, raw)
	}
	req, _ := http.NewRequest("GET", e.user.URL+"/preview/"+id+"/8000/app/index.html", nil)
	for _, ck := range alice.hc.Jar.Cookies(mustURL(e.user.URL)) {
		req.AddCookie(ck)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen := resp.Header.Get("X-Seen-Path"); seen != "/app/index.html" {
		t.Fatalf("the dev server saw the path %q", seen)
	}
	if strings.Contains(resp.Header.Get("X-Seen-Cookie"), "syx_") || resp.Header.Get("X-Seen-Cookie") != "" {
		t.Fatalf("the Syntaxis session cookie was forwarded to user code: %q", resp.Header.Get("X-Seen-Cookie"))
	}
	if resp.Header.Get("Set-Cookie") != "" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("previewed content must be sandboxed and unable to set cookies on our origin")
	}
	if resp.Header.Get("Clear-Site-Data") != "" || resp.Header.Get("Service-Worker-Allowed") != "" {
		t.Fatal("previewed content must not be able to clear our site data or widen a service worker's scope")
	}
	if code, _, _ := bob.do("GET", "/preview/"+id+"/8000/", nil); code != 404 {
		t.Fatalf("another user reached the preview: %d", code)
	}
	if code, _, _ := alice.do("GET", "/preview/"+id+"/80/", nil); code != 400 {
		t.Fatalf("privileged port allowed: %d", code)
	}
	anon := e.client(e.user.URL)
	if code, _, _ := anon.do("GET", "/preview/"+id+"/8000/", nil); code != 401 {
		t.Fatalf("unauthenticated preview: %d", code)
	}
}

func TestBenchmarkRoutesAreAdminOnly(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	usr := e.userSession("a@example.com")
	for _, p := range []string{"/api/models/1/benchmark", "/api/benchmarks", "/api/models/1/recommended"} {
		if code, _, _ := usr.do("POST", p, map[string]bool{"recommended": true}); code == 200 || code == 202 {
			t.Fatalf("chat user reached admin benchmark route %s: %d", p, code)
		}
	}
}

// A real benchmark of the real tiny model in a throwaway real sandbox. The tiny model passing few
// tasks is a valid result: what is checked is that a report is stored, shown to admins only, and
// the throwaway sandbox is gone.
func TestLiveBenchmarkStoresResultAndCleansUp(t *testing.T) {
	cfg := testenv.Load(t)
	if cfg["OLLAMA_URL"] == "" || cfg["MODEL_TINY"] == "" {
		testenv.NotTested(t, "OLLAMA_URL and MODEL_TINY are not in the private test environment")
	}
	e := newEnv(t)
	e.app.Sandbox = realSandbox(t, e)
	ctx := context.Background()
	pid, _ := e.st.UpsertProvider(ctx, store.Provider{Name: "local", Kind: cfg["OLLAMA_KIND"], BaseURL: cfg["OLLAMA_URL"], Concurrency: 1, Enabled: true})
	mid, _ := e.st.UpsertModel(ctx, store.Model{ProviderID: pid, Name: cfg["MODEL_TINY"], ToolsSupported: 1})
	_ = e.st.SetModelEnabled(ctx, mid, true)
	t.Cleanup(func() { _ = e.app.Sandbox.RT.RemoveVolume(ctx, sandbox.WorkspaceVolume(0)) })

	usr := e.userSession("a@example.com")
	adm := e.makeAdminSession()
	if code, _, raw := adm.do("POST", "/api/models/"+strconv.FormatInt(mid, 10)+"/benchmark", nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	var list []map[string]any
	deadline := time.Now().Add(25 * time.Minute)
	for time.Now().Before(deadline) {
		_, _, raw := adm.do("GET", "/api/benchmarks", nil)
		_ = json.Unmarshal(raw, &list)
		if len(list) == 1 {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if len(list) != 1 || list[0]["report"] == nil {
		t.Fatalf("no benchmark stored: %v", list)
	}
	if code, _, _ := adm.do("POST", "/api/models/"+strconv.FormatInt(mid, 10)+"/recommended", map[string]bool{"recommended": true}); code != 200 {
		t.Fatal("recommend")
	}
	// the "recommended" flag is visible to users; the report is not
	_, _, raw := usr.do("GET", "/api/models", nil)
	if !strings.Contains(string(raw), `"recommended":true`) || strings.Contains(string(raw), "pass_rate") {
		t.Fatalf("%s", raw)
	}
	cs, _ := e.app.Sandbox.RT.List(ctx)
	for _, c := range cs {
		if strings.HasPrefix(c.Labels[sandbox.LabelSession], "bench-") && c.Labels[sandbox.LabelUser] == "0" {
			t.Fatal("the throwaway benchmark sandbox was not destroyed")
		}
	}
}

func TestMergeEnvOwnEntriesWinAndOrderIsStable(t *testing.T) {
	got := mergeEnv([]string{"A=own", "TERM=x"}, map[string]string{"A": "secret", "B": "2", "C": "3"})
	if strings.Join(got, " ") != "A=own TERM=x B=2 C=3" {
		t.Fatalf("%v", got)
	}
}
