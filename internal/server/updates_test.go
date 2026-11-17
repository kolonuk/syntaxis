// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/testenv"
	"github.com/kolonuk/syntaxis/internal/update"
)

// The admin sees what is running and what is available from the real GitHub API, and can ask for an
// update only when the host's update service is there to do it.
func TestAdminUpdateInfoAndRequestRules(t *testing.T) {
	testenv.Network(t, "api.github.com:443")
	e := newEnv(t)
	e.app.Updates = &update.Source{Repo: "docker/compose"} // a real repository with releases and a main branch
	usr := e.userSession("a@example.com")
	adm := e.makeAdminSession()
	if code, _, _ := usr.do("GET", "/api/update", nil); code == 200 {
		t.Fatal("a chat user read the update page")
	}
	code, m, raw := adm.do("GET", "/api/update", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if rs, _ := m["releases"].([]any); len(rs) == 0 || m["latest"] == nil {
		t.Fatalf("no releases found: %s", raw)
	}
	if m["edge"] == nil || m["agent"].(map[string]any)["running"] != false || m["mode"] != "edge" {
		t.Fatalf("%s", raw)
	}
	latest := m["latest"].(map[string]any)["tag"].(string)

	for _, bad := range []string{"", "main", "v1.2", "v1.2.3; reboot", "$(id)", "edge now"} {
		if code, _, _ := adm.do("POST", "/api/update", map[string]string{"target": bad}); code != 400 {
			t.Errorf("%q: %d", bad, code)
		}
	}
	// nothing on the host is listening yet
	if code, m, _ := adm.do("POST", "/api/update", map[string]string{"target": "edge"}); code != 409 || !strings.Contains(m["error"].(string), "scripts/update.sh") {
		t.Fatalf("without the host service the admin is told how to update by hand: %d %v", code, m)
	}
	if code, _, _ := usr.do("POST", "/api/update", map[string]string{"target": "edge"}); code == 202 {
		t.Fatal("a chat user asked for an update")
	}
	_ = latest
}

// The real host scripts against the real server: the update service polls, is handed the request once,
// runs the real update.sh (as a dry run, so nothing on this machine changes) and reports back.
func TestHostUpdateServiceRunsARequestedUpdate(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		testenv.NotTested(t, "curl is not installed")
	}
	testenv.Network(t, "api.github.com:443")
	e := newEnv(t)
	e.app.Cfg.FirewallToken = "host-token-for-tests"
	e.app.Updates = &update.Source{Repo: "docker/compose"}
	adm := e.makeAdminSession()
	agent := func() (string, error) {
		cmd := exec.Command("sh", "../../scripts/update-agent.sh", "--once")
		cmd.Env = append(cmd.Environ(), "SYNTAXIS_FIREWALL_TOKEN=host-token-for-tests", "UPDATE_URL="+e.admin.URL, "UPDATE_EXTRA_ARGS=--dry-run")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// a wrong token is refused, and nothing is handed out
	if out, _ := exec.Command("curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Authorization: Bearer wrong", e.admin.URL+"/internal/update").Output(); string(out) != "401" {
		t.Fatalf("a wrong host token: %s", out)
	}
	// the first poll shows the service is there
	if out, err := agent(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_, info, _ := adm.do("GET", "/api/update", nil)
	if info["agent"].(map[string]any)["running"] != true {
		t.Fatalf("%v", info["agent"])
	}
	for _, target := range []string{"edge", "latest"} {
		if target == "latest" {
			// the status of the finished request must be settled before the next one
			_, st, _ := adm.do("GET", "/api/update", nil)
			if st["status"].(map[string]any)["state"] != "done" {
				t.Fatalf("%v", st["status"])
			}
		}
		code, m, raw := adm.do("POST", "/api/update", map[string]string{"target": target})
		if code != 202 {
			t.Fatalf("%s: %d %s", target, code, raw)
		}
		if code, _, _ := adm.do("POST", "/api/update", map[string]string{"target": "edge"}); code != 409 {
			t.Fatalf("a second request while one is waiting: %d", code)
		}
		if out, err := agent(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		_, st, _ := adm.do("GET", "/api/update", nil)
		s := st["status"].(map[string]any)
		want := "channel=edge"
		if target == "latest" {
			want = "channel=release target=" + m["target"].(string)
		}
		if s["state"] != "done" || !strings.Contains(s["message"].(string), want) {
			t.Fatalf("%s: %v (want %q)", target, s, want)
		}
	}
	// handed out once: another poll finds nothing to do
	if out, err := agent(); err != nil || strings.Contains(out, "plan:") {
		t.Fatalf("a request ran twice: %v %s", err, out)
	}
	t.Log("NOT TESTED: a real update (pull or build, restart, health check, rollback); that needs a disposable Docker host")
}
