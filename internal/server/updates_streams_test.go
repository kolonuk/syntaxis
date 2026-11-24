// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/testenv"
	"github.com/kolonuk/syntaxis/internal/update"
)

func TestResolveUpdateTarget(t *testing.T) {
	rs := []update.Release{
		{Tag: "v0.3.0-rc.1", Prerelease: true}, {Tag: "v0.2.1"}, {Tag: "v0.2.0"},
	}
	for _, tc := range []struct {
		name, target, stream string
		releases             []update.Release
		wantTag, wantStream  string
		wantErr              bool
	}{
		{"stable follows the newest non-pre-release", "stable", "", rs, "v0.2.1", "stable", false},
		{"latest is the old name for stable", "latest", "", rs, "v0.2.1", "stable", false},
		{"preview follows the newest of all", "preview", "", rs, "v0.3.0-rc.1", "preview", false},
		{"edge needs no release", "edge", "", nil, "edge", "edge", false},
		{"a stable tag by hand stays on stable", "v0.2.0", "", rs, "v0.2.0", "stable", false},
		{"a pre-release tag by hand is on preview", "v0.3.0-rc.1", "", rs, "v0.3.0-rc.1", "preview", false},
		{"a stable tag can be run on the preview stream", "v0.2.1", "preview", rs, "v0.2.1", "preview", false},
		{"a pre-release cannot be on stable", "v0.3.0-rc.1", "stable", rs, "", "", true},
		{"a tag cannot be on edge", "v0.2.1", "edge", rs, "", "", true},
		{"an unknown stream", "v0.2.1", "nightly", rs, "", "", true},
		{"stable with only pre-releases", "stable", "", []update.Release{{Tag: "v1.0.0-rc.1", Prerelease: true}}, "", "", true},
		{"no releases at all", "preview", "", nil, "", "", true},
		{"nonsense", "main", "", rs, "", "", true},
	} {
		tag, stream, msg := resolveUpdateTarget(tc.releases, tc.target, tc.stream)
		if (msg != "") != tc.wantErr || tag != tc.wantTag || stream != tc.wantStream {
			t.Errorf("%s: got %q %q %q, want %q %q (error %v)", tc.name, tag, stream, msg, tc.wantTag, tc.wantStream, tc.wantErr)
		}
	}
	for target, want := range map[string]string{"edge": "edge", "v1.0.0-rc.1": "preview", "v1.0.0": "stable"} {
		if got := impliedStream(target); got != want {
			t.Errorf("impliedStream(%q) = %q, want %q", target, got, want)
		}
	}
}

// The admin app learns the streams and what each would install, and a request carries the stream to
// the host only when the target does not already imply it, so an older host service still works.
func TestUpdateStreamsInTheAdminAppAndTheHostProtocol(t *testing.T) {
	testenv.Network(t, "api.github.com:443")
	e := newEnv(t)
	e.app.Cfg.FirewallToken = "host-token-for-tests"
	e.app.Updates = &update.Source{Repo: "docker/compose"} // a real repository with stable and pre-releases
	adm := e.makeAdminSession()

	code, info, raw := adm.do("GET", "/api/update", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	skipIfGitHubLimited(t, info)
	streams, _ := info["streams"].([]any)
	if len(streams) != 3 || info["stream"] != "edge" {
		t.Fatalf("a dev build is on edge and all three streams are listed: %s", raw)
	}
	byID := map[string]map[string]any{}
	for _, s := range streams {
		m := s.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if byID["stable"]["latest"] == nil || byID["preview"]["latest"] == nil || byID["edge"]["current"] != true {
		t.Fatalf("streams: %v", streams)
	}
	if byID["stable"]["latest"].(map[string]any)["prerelease"] == true {
		t.Fatal("stable offered a pre-release")
	}

	poll := func() string {
		req, _ := http.NewRequest("GET", e.admin.URL+"/internal/update", nil)
		req.Header.Set("Authorization", "Bearer host-token-for-tests")
		req.Header.Set("X-Syntaxis-Agent", "2") // the current host service says which format it understands
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return strings.TrimSpace(string(b))
	}
	poll() // the first poll is what tells the app the host service is there

	// stable: the tag alone says it all
	if code, m, raw := adm.do("POST", "/api/update", map[string]string{"target": "stable"}); code != 202 || m["stream"] != "stable" {
		t.Fatalf("stable: %d %s", code, raw)
	}
	got := poll()
	tag := strings.TrimPrefix(got, "request ")
	if !strings.HasPrefix(got, "request ") || strings.Contains(tag, " ") || !update.ValidTag(tag) {
		t.Fatalf("a stable request is the one-word form an older host service knows: %q", got)
	}
	finish(t, e, adm)

	// a stable version on the preview stream needs the stream spelled out
	if code, _, raw := adm.do("POST", "/api/update", map[string]string{"target": tag, "stream": "preview"}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	if got := poll(); got != "request "+tag+" preview" {
		t.Fatalf("the host must be told the stream: %q", got)
	}
	finish(t, e, adm)

	// edge, and the rules that cannot be combined
	if code, _, _ := adm.do("POST", "/api/update", map[string]string{"target": "edge"}); code != 202 {
		t.Fatal("edge")
	}
	if got := poll(); got != "request edge" {
		t.Fatalf("edge: %q", got)
	}
	finish(t, e, adm)
	if code, _, _ := adm.do("POST", "/api/update", map[string]string{"target": tag, "stream": "edge"}); code != 400 {
		t.Fatalf("a tag on edge: %d", code)
	}
	if code, _, _ := adm.do("POST", "/api/update", map[string]string{"target": "stable", "stream": "nightly"}); code != 400 {
		t.Fatalf("an unknown stream: %d", code)
	}
}

// finish reports the host's update as done, so the next request is not refused as "in progress".
func finish(t *testing.T, e *env, adm *client) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.admin.URL+"/internal/update/status", strings.NewReader("done ok"))
	req.Header.Set("Authorization", "Bearer host-token-for-tests")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, st, _ := adm.do("GET", "/api/update", nil)
	if st["status"].(map[string]any)["state"] != "done" {
		t.Fatalf("%v", st["status"])
	}
}

// The real update.sh in dry-run mode says which stream and version it would use. The "preview"
// answer comes from this project's real GitHub releases.
func TestUpdateScriptPlansTheRightStream(t *testing.T) {
	testenv.Network(t, "api.github.com:443")
	run := func(args ...string) (string, error) {
		out, err := exec.Command("sh", append([]string{"../../scripts/update.sh", "--dry-run"}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	for _, tc := range []struct{ args, want string }{
		{"--release v0.0.1", "plan: channel=release target=v0.0.1 stream=stable"},
		{"--release v0.0.1-rc.1", "plan: channel=release target=v0.0.1-rc.1 stream=preview"},
		{"--release v0.0.1 --stream preview", "plan: channel=release target=v0.0.1 stream=preview"},
		{"--edge", "plan: channel=edge target=main stream=edge"},
		{"--stream edge", "plan: channel=edge target=main stream=edge"},
	} {
		out, err := run(strings.Fields(tc.args)...)
		if err != nil || !strings.HasPrefix(out, tc.want) {
			t.Errorf("%s: %v %q (want %q)", tc.args, err, out, tc.want)
		}
	}
	if out, err := run("--stream", "nightly"); err == nil || !strings.Contains(out, "unknown stream") {
		t.Errorf("an unknown stream must be refused: %v %q", err, out)
	}
	// the real releases: stable is never a pre-release, and preview is never older than stable
	stable, err := run("--stream", "stable")
	if err != nil {
		t.Skipf("GitHub did not answer: %s", stable)
	}
	preview, _ := run("--stream", "preview")
	if strings.Contains(preview, "target=unknown") {
		testenv.NotTested(t, "GitHub did not list the releases for the preview stream (rate limited?): "+preview)
	}
	tag := func(plan string) string {
		for _, f := range strings.Fields(plan) {
			if v, ok := strings.CutPrefix(f, "target="); ok {
				return v
			}
		}
		return ""
	}
	st, pv := tag(stable), tag(preview)
	if !update.ValidTag(st) || strings.Contains(st, "-") || !update.ValidTag(pv) || update.Newer(st, pv) {
		t.Errorf("stable %q preview %q", st, pv)
	}
}

// The real host service, handed a stream, runs the real update script with it (as a dry run).
func TestHostServiceCarriesTheStreamToTheScript(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		testenv.NotTested(t, "curl is not installed")
	}
	testenv.Network(t, "api.github.com:443")
	e := newEnv(t)
	e.app.Cfg.FirewallToken = "host-token-for-tests"
	e.app.Updates = &update.Source{Repo: "docker/compose"}
	adm := e.makeAdminSession()
	agent := func() string {
		cmd := exec.Command("sh", "../../scripts/update-agent.sh", "--once")
		cmd.Env = append(cmd.Environ(), "SYNTAXIS_FIREWALL_TOKEN=host-token-for-tests", "UPDATE_URL="+e.admin.URL, "UPDATE_EXTRA_ARGS=--dry-run")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return string(out)
	}
	agent() // announce the host service
	_, info, _ := adm.do("GET", "/api/update", nil)
	skipIfGitHubLimited(t, info)
	for _, tc := range []struct {
		body map[string]string
		want string
	}{
		{map[string]string{"target": "preview"}, "stream=preview"},
		{map[string]string{"target": "stable"}, "stream=stable"},
		{map[string]string{"target": "v0.0.1", "stream": "preview"}, "target=v0.0.1 stream=preview"},
		{map[string]string{"target": "edge"}, "channel=edge"},
	} {
		if code, _, raw := adm.do("POST", "/api/update", tc.body); code != 202 {
			t.Fatalf("%v: %d %s", tc.body, code, raw)
		}
		agent()
		_, st, _ := adm.do("GET", "/api/update", nil)
		s := st["status"].(map[string]any)
		if s["state"] != "done" || !strings.Contains(s["message"].(string), tc.want) {
			t.Fatalf("%v: %v (want %q)", tc.body, s, tc.want)
		}
	}
	// the host service refuses a stream it does not know, whatever the server says
	e.app.Store.SetSetting(t.Context(), settingUpdateRequest, `{"target":"v0.1.0","stream":"nightly","at":"2026-01-01T00:00:00Z"}`)
	if out := agent(); strings.Contains(out, "plan:") {
		t.Fatalf("an unknown stream reached the update script: %s", out)
	}
}

// A host service from before streams sends no protocol header and knows only the one-word request.
// Asking it to follow a stream that the target does not imply must be refused with the way out,
// not handed over to be silently rejected; the usual requests still work.
func TestAnOlderHostServiceIsToldToRestartForAStreamRequest(t *testing.T) {
	testenv.Network(t, "api.github.com:443")
	e := newEnv(t)
	e.app.Cfg.FirewallToken = "host-token-for-tests"
	e.app.Updates = &update.Source{Repo: "docker/compose"}
	adm := e.makeAdminSession()
	poll := func(header string) string {
		req, _ := http.NewRequest("GET", e.admin.URL+"/internal/update", nil)
		req.Header.Set("Authorization", "Bearer host-token-for-tests")
		if header != "" {
			req.Header.Set("X-Syntaxis-Agent", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return strings.TrimSpace(string(b))
	}
	poll("") // an older service announces itself with no header
	_, info, _ := adm.do("GET", "/api/update", nil)
	skipIfGitHubLimited(t, info)
	rs, _ := info["releases"].([]any)
	if len(rs) == 0 {
		t.Skip("no releases to ask for")
	}
	// the target implies its stream: the one-word form, which the older service understands
	if code, _, raw := adm.do("POST", "/api/update", map[string]string{"target": "edge"}); code != 202 {
		t.Fatalf("edge from an older service: %d %s", code, raw)
	}
	if got := poll(""); got != "request edge" {
		t.Fatalf("%q", got)
	}
	finish(t, e, adm)
	// a stream the target does not imply: refused with the way out
	tag := rs[0].(map[string]any)["tag"].(string)
	code, m, _ := adm.do("POST", "/api/update", map[string]string{"target": tag, "stream": "preview", "x": ""})
	if strings.Contains(tag, "-") {
		tag = "" // a pre-release already implies preview; nothing to refuse
	}
	if tag != "" && (code != 409 || !strings.Contains(m["error"].(string), "systemctl restart syntaxis-update-agent")) {
		t.Fatalf("an older service cannot take a stream request: %d %v", code, m)
	}
	// once the service announces the new protocol the same request goes through
	poll("2")
	if tag != "" {
		if code, _, raw := adm.do("POST", "/api/update", map[string]string{"target": tag, "stream": "preview"}); code != 202 {
			t.Fatalf("after the service restarted: %d %s", code, raw)
		}
	}
	// a malformed header is not a newer protocol
	if hostProtocolValue("abc") != 0 || hostProtocolValue("") != 0 || hostProtocolValue("2") != 2 || hostProtocolValue("999") != 0 {
		t.Fatal("protocol header parsing")
	}
}

func hostProtocolValue(v string) int {
	r, _ := http.NewRequest("GET", "http://x/", nil)
	if v != "" {
		r.Header.Set("X-Syntaxis-Agent", v)
	}
	return hostProtocol(r)
}

// The weekly timer must never move an install backwards: a preview install must not be pulled
// back to an older stable. The comparison is the real function out of update.sh.
func TestUpdateScriptComparesVersionsLikeSemver(t *testing.T) {
	src, err := os.ReadFile("../../scripts/update.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	i := strings.Index(text, "newer() {")
	j := strings.Index(text[i:], "\n}\n")
	if i < 0 || j < 0 {
		t.Fatal("newer() not found in update.sh")
	}
	fn := text[i : i+j+3]
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v0.2.1", "0.2.0", true}, {"v0.2.0", "v0.2.0", false}, {"v0.2.0", "v0.2.1", false},
		{"v0.10.0", "v0.9.0", true}, {"v1.0.0", "v0.99.9", true},
		{"v0.3.0", "v0.3.0-rc.2", true}, {"v0.3.0-rc.2", "v0.3.0", false}, {"v0.3.0-rc.2", "v0.3.0-rc.1", true},
		{"v0.3.0-rc.1", "v0.2.9", true}, {"v0.2.9", "v0.3.0-rc.1", false},
	} {
		out, err := exec.Command("sh", "-c", fn+`
if newer "$1" "$2"; then echo yes; else echo no; fi`, "sh", tc.a, tc.b).Output()
		got := strings.TrimSpace(string(out))
		if err != nil || (got == "yes") != tc.want {
			t.Errorf("newer(%s, %s) = %q (%v), want %v", tc.a, tc.b, got, err, tc.want)
		}
	}
}

// An update restarts the server, and a rollback does too, so the host service often cannot deliver
// its final report. The service does one thing at a time: when it asks for work again, the run it
// was doing has ended. The page must not show "running" for ever.
func TestAnUpdateWhoseResultWasNeverReportedDoesNotStayRunning(t *testing.T) {
	e := newEnv(t)
	e.app.Cfg.FirewallToken = "host-token-for-tests"
	adm := e.makeAdminSession()
	poll := func() string {
		req, _ := http.NewRequest("GET", e.admin.URL+"/internal/update", nil)
		req.Header.Set("Authorization", "Bearer host-token-for-tests")
		req.Header.Set("X-Syntaxis-Agent", "2")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return strings.TrimSpace(string(b))
	}
	poll()
	if code, _, raw := adm.do("POST", "/api/update", map[string]string{"target": "edge"}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	if got := poll(); got != "request edge" {
		t.Fatalf("%q", got)
	}
	state := func() (string, string) {
		_, st, _ := adm.do("GET", "/api/update", nil)
		s := st["status"].(map[string]any)
		msg, _ := s["message"].(string)
		return s["state"].(string), msg
	}
	if s, _ := state(); s != "running" {
		t.Fatalf("after hand-out: %s", s)
	}
	// no report comes (the server was restarting); the service asks again
	if got := poll(); got != "none" {
		t.Fatalf("%q", got)
	}
	s, msg := state()
	if s != "unknown" || !strings.Contains(msg, "without telling Syntaxis") || !strings.Contains(msg, "Running now") {
		t.Fatalf("the lost result must be said plainly: %s %q", s, msg)
	}
	// and it can be asked again straight away
	if code, _, raw := adm.do("POST", "/api/update", map[string]string{"target": "edge"}); code != 202 {
		t.Fatalf("a new request after an unknown result: %d %s", code, raw)
	}
	es, _ := e.st.ListAudit(t.Context(), 0)
	found := false
	for _, x := range es {
		found = found || x.Action == "update.unknown"
	}
	if !found {
		t.Fatal("the lost result was not audited")
	}
}
