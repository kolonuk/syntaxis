// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/environments"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

// hostService runs the real host update service once against the test server's admin address.
func hostService(t *testing.T, e *env, extra ...string) string {
	t.Helper()
	cmd := exec.Command("sh", "../../scripts/update-agent.sh", "--once")
	cmd.Env = append(cmd.Environ(), "SYNTAXIS_FIREWALL_TOKEN=host-token-for-tests", "UPDATE_URL="+e.admin.URL)
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

func envState(t *testing.T, adm *client, name string) map[string]any {
	t.Helper()
	_, m, raw := adm.do("GET", "/api/environment-catalog", nil)
	for _, x := range m["environments"].([]any) {
		if row := x.(map[string]any); row["name"] == name {
			return row
		}
	}
	t.Fatalf("%s is not in the catalog: %s", name, raw)
	return nil
}

// The catalog, who may build, the host service protocol, and approval of a finished build: the real
// host scripts against the real server, as a dry run so no image is built on this machine.
func TestEnvironmentBuildIsOnRequestAndApprovesWhatWasBuilt(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		testenv.NotTested(t, "curl is not installed")
	}
	e := newEnv(t)
	e.app.Cfg.FirewallToken = "host-token-for-tests"
	usr := e.userSession("a@example.com")
	adm := e.makeAdminSession()

	if code, _, _ := usr.do("GET", "/api/environment-catalog", nil); code == 200 {
		t.Fatal("a chat user read the catalog")
	}
	if code, _, _ := usr.do("POST", "/api/environment-catalog/zig/build", nil); code == 202 {
		t.Fatal("a chat user started a build")
	}
	_, m, _ := adm.do("GET", "/api/environment-catalog", nil)
	if len(m["environments"].([]any)) != len(environments.Catalog) || m["agent_running"] != false || m["idle_days"] != float64(30) {
		t.Fatalf("%v", m)
	}
	if st := envState(t, adm, "zig"); st["state"] != "not built" {
		t.Fatalf("%v", st)
	}
	// nothing is built unless asked, and only catalog names are accepted
	if code, _, _ := adm.do("POST", "/api/environment-catalog/not-a-thing/build", nil); code != 404 {
		t.Fatalf("%d", code)
	}
	if code, m, _ := adm.do("POST", "/api/environment-catalog/zig/build", nil); code != 409 || !strings.Contains(m["error"].(string), "build-templates.sh") {
		t.Fatalf("without the host service the admin is told how to build by hand: %d %v", code, m)
	}
	hostService(t, e, "BUILD_EXTRA_ARGS=--dry-run") // the service shows itself
	if code, _, raw := adm.do("POST", "/api/environment-catalog/zig/build", nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	if st := envState(t, adm, "zig"); st["state"] != "queued" {
		t.Fatalf("%v", st)
	}
	if code, _, _ := adm.do("POST", "/api/environment-catalog/zig/build", nil); code != 409 {
		t.Fatalf("a second request while one is waiting: %d", code)
	}
	hostService(t, e, "BUILD_EXTRA_ARGS=--dry-run")
	// the finished build is approved, with the catalog's description and devcontainer matches
	ts, _ := e.st.ListImageTemplates(context.Background())
	if len(ts) != 1 || ts[0].Name != "zig" || ts[0].Image != environments.Image("zig") || !ts[0].Managed {
		t.Fatalf("%+v", ts)
	}
	// handed out once
	if out := hostService(t, e, "BUILD_EXTRA_ARGS=--dry-run"); strings.Contains(out, "==>") {
		t.Fatalf("a task ran twice: %s", out)
	}
	// a wrong token and a malformed status are refused
	if out, _ := exec.Command("curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Authorization: Bearer wrong", e.admin.URL+"/internal/environments").Output(); string(out) != "401" {
		t.Fatalf("%s", out)
	}
	// removal
	if code, _, raw := adm.do("POST", "/api/environment-catalog/zig/remove", nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	hostService(t, e, "BUILD_EXTRA_ARGS=--dry-run")
	if ts, _ := e.st.ListImageTemplates(context.Background()); len(ts) != 0 {
		t.Fatalf("a removed environment is still approved: %+v", ts)
	}
	if code, _, _ := adm.do("PUT", "/api/environment-settings", map[string]int{"idle_days": -1}); code != 400 {
		t.Fatalf("%d", code)
	}
	if code, m, _ := adm.do("PUT", "/api/environment-settings", map[string]int{"idle_days": 7}); code != 200 || m["idle_days"] != float64(7) {
		t.Fatalf("%d %v", code, m)
	}
}

// A real build of a real (small) environment through the host service, a real sandbox in it, and the
// cleanup of an unused one: an environment in use is kept, and an idle one is removed from Docker.
func TestEnvironmentRealBuildUseAndCleanup(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	e.app.Sandbox = realSandbox(t, e)
	cleanupSandboxes(t, e)
	image := environments.Image("typescript")
	if err := exec.Command("docker", "image", "inspect", image).Run(); err == nil {
		testenv.NotTested(t, image+" already exists on this Docker host; not touching it")
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })
	e.app.Cfg.FirewallToken = "host-token-for-tests"
	adm := e.makeAdminSession()
	usr := e.userSession("a@example.com")
	hostService(t, e) // the service shows itself
	if code, _, raw := adm.do("POST", "/api/environment-catalog/typescript/build", nil); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	out := hostService(t, e, "BASE="+sandbox.DefaultConfig().Image) // really builds
	if !strings.Contains(out, "Built:") && !strings.Contains(out, "typescript") {
		t.Logf("service output: %s", out)
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Fatalf("the image was not built: %s", out)
	}
	if st := envState(t, adm, "typescript"); st["state"] != "built" || st["managed"] != true {
		t.Fatalf("%v", st)
	}
	// users see it and can start a session in it
	_, _, raw := usr.do("GET", "/api/environments", nil)
	if !strings.Contains(string(raw), "typescript") {
		t.Fatalf("%s", raw)
	}
	code, s, raw2 := usr.do("POST", "/api/sessions", map[string]any{"model": "prov/m1", "template": "typescript"})
	if code != 201 {
		t.Fatalf("%d %s", code, raw2)
	}
	ctx := context.Background()
	id := s["id"].(string)
	sess, _ := e.st.GetChatSession(ctx, id)
	u, _ := e.st.GetUser(ctx, sess.UserID)
	cid, err := e.app.ensureSandbox(ctx, sess, u)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := e.app.Sandbox.Exec(ctx, id, cid, execSpec("tsc --version")); !strings.Contains(r.Stdout, "Version") {
		t.Fatalf("the sandbox does not have the toolchain: %+v", r)
	}

	// idle for longer than the limit, but a sandbox still uses it: kept
	old := e.now.Add(-40 * 24 * time.Hour) // the test server keeps its own clock
	_ = e.st.TouchImageTemplate(ctx, image, old)
	e.app.environmentsTick(ctx)
	if tasks := e.app.envTasks(ctx); len(tasks) != 0 {
		t.Fatalf("an environment in use was scheduled for removal: %+v", tasks)
	}
	// the session goes; now it is removed
	_ = e.app.Sandbox.Destroy(ctx, id, cid)
	_ = e.st.UpdateChatSession(ctx, store.ChatSession{ID: id, Title: "x", ProviderID: sess.ProviderID, Model: sess.Model, Autonomy: sess.Autonomy, State: "idle", SandboxID: "", UpdatedAt: time.Now()})
	e.app.environmentsTick(ctx)
	if tasks := e.app.envTasks(ctx); len(tasks) != 1 || tasks[0].Action != "remove" {
		t.Fatalf("an idle environment was not scheduled for removal: %+v", tasks)
	}
	hostService(t, e)
	if err := exec.Command("docker", "image", "inspect", image).Run(); err == nil {
		t.Fatal("the unused image is still on the Docker host")
	}
	if ts, _ := e.st.ListImageTemplates(ctx); len(ts) != 0 {
		t.Fatalf("%+v", ts)
	}
	// 0 keeps everything for ever
	_ = e.st.UpsertImageTemplate(ctx, store.ImageTemplate{Name: "go", Image: environments.Image("go"), Managed: true, LastUsed: old})
	_ = e.st.SetSetting(ctx, settingEnvIdleDays, "0")
	e.app.environmentsTick(ctx)
	if tasks := e.app.envTasks(ctx); len(tasks) != 0 {
		t.Fatalf("%+v", tasks)
	}
}
