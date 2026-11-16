// SPDX-License-Identifier: GPL-2.0-or-later

package dockerproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/testenv"
)

// These tests run the proxy in front of the real Docker daemon, with real gVisor containers.
// A refusal is checked two ways: the proxy answers 403, and the daemon was never contacted (for
// the cases where the proxy decides on its own) or the real container is untouched.

const (
	sock  = "/var/run/docker.sock"
	image = "syntaxis-sandbox:latest"
)

var seq atomic.Int64

// fx is a real Docker environment for one test: its own network, volumes and containers, all
// removed afterwards, and a proxy in front of the daemon.
type fx struct {
	t       *testing.T
	id      string
	net     string
	srv     *httptest.Server
	dials   atomic.Int64
	dnsPath string
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func dockerQuiet(args ...string) { _ = exec.Command("docker", args...).Run() }

func newFx(t *testing.T) *fx {
	t.Helper()
	testenv.GVisor(t)
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		testenv.NotTested(t, "the sandbox image "+image+" is not built")
	}
	f := &fx{t: t, id: fmt.Sprintf("ptest%d%d", time.Now().UnixNano()%1e8, seq.Add(1))}
	f.net = "syntaxis-" + f.id + "-net"
	docker(t, "network", "create", f.net)
	t.Cleanup(func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=io.syntaxis.test="+f.id).Output()
		for _, c := range strings.Fields(string(out)) {
			dockerQuiet("rm", "-f", c)
		}
		dockerQuiet("network", "rm", f.net)
		dockerQuiet("volume", "rm", "-f", "syntaxis-ws-u"+f.userID())
	})
	f.srv = httptest.NewServer(New(Config{Network: f.net, Dial: func(context.Context) (net.Conn, error) {
		f.dials.Add(1)
		return net.Dial("unix", sock)
	}}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fx) userID() string { return "9" + strings.TrimPrefix(f.id, "ptest") }

// container makes a real container directly on the daemon (not through the proxy) and returns its id.
func (f *fx) container(labels map[string]string, extra ...string) string {
	f.t.Helper()
	args := []string{"create", "--label", "io.syntaxis.test=" + f.id}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, extra...)
	return docker(f.t, args...)
}

func (f *fx) running(id string) bool {
	out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", id).Output()
	return strings.TrimSpace(string(out)) == "true"
}

func (f *fx) exists(kind, name string) bool {
	return exec.Command("docker", kind, "inspect", name).Run() == nil
}

func (f *fx) do(method, path string, body any) (int, []byte) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (f *fx) refused(why, method, path string, body any) {
	f.t.Helper()
	if code, b := f.do(method, path, body); code != http.StatusForbidden {
		f.t.Errorf("%s: got %d, want 403 (%s)", why, code, b)
	}
}

// raw talks to the daemon directly, for setting up what the proxy is then asked about.
func raw(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", sock) }}}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://docker"+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// sandboxBody is what Syntaxis sends for a session sandbox.
func (f *fx) sandboxBody() map[string]any {
	// the resolver file the real manager mounts from the DNS volume
	if f.dnsPath == "" {
		docker(f.t, "run", "--rm", "--user", "0", "-v", DNSVolume+":/d", image, "sh", "-c", "touch /d/resolv.conf")
		f.dnsPath = docker(f.t, "volume", "inspect", "-f", "{{.Mountpoint}}", DNSVolume)
	}
	mp := f.dnsPath
	return map[string]any{
		"Image": image, "User": "10001:10001", "Cmd": []string{"sleep", "infinity"},
		"Labels": map[string]string{"io.syntaxis.managed": "true", "io.syntaxis.user": f.userID(), "io.syntaxis.test": f.id},
		"HostConfig": map[string]any{
			"Runtime": "runsc", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"},
			"Memory": 256 << 20, "NanoCPUs": 1e9, "PidsLimit": 64, "NetworkMode": f.net,
			"Tmpfs": map[string]string{"/tmp": "rw"},
			"Mounts": []map[string]any{
				{"Type": "volume", "Source": "syntaxis-ws-u" + f.userID(), "Target": "/workspace"},
				{"Type": "bind", "Source": mp + "/resolv.conf", "Target": "/etc/resolv.conf", "ReadOnly": true},
			},
		},
	}
}

func TestSandboxCreateReachesTheRealDaemonAndRuns(t *testing.T) {
	f := newFx(t)
	code, b := f.do("POST", "/v1.47/containers/create?name=syntaxis-"+f.id, f.sandboxBody())
	if code != 201 {
		t.Fatalf("a valid sandbox was refused: %d %s", code, b)
	}
	var created struct{ Id string }
	_ = json.Unmarshal(b, &created)
	if code, b := f.do("POST", "/containers/"+created.Id+"/start", nil); code != 204 {
		t.Fatalf("start: %d %s", code, b)
	}
	if !f.running(created.Id) {
		t.Fatal("the container is not running")
	}
	if got := docker(t, "inspect", "-f", "{{.HostConfig.Runtime}} {{.HostConfig.Privileged}} {{.HostConfig.ReadonlyRootfs}}", created.Id); got != "runsc false true" {
		t.Fatalf("what the daemon made: %s", got)
	}
	// a command inside it through the proxy, as the agent's does
	code, b = f.do("POST", "/containers/"+created.Id+"/exec", map[string]any{"Cmd": []string{"id", "-u"}, "AttachStdout": true})
	if code != 201 {
		t.Fatalf("exec create: %d %s", code, b)
	}
	if code, b := f.do("POST", "/containers/"+created.Id+"/exec", map[string]any{"Cmd": []string{"id"}, "User": "root"}); code != 403 {
		t.Errorf("root exec allowed: %d %s", code, b)
	}
	if code, _ := f.do("POST", "/containers/"+created.Id+"/exec", map[string]any{"Cmd": []string{"id"}, "Privileged": true}); code != 403 {
		t.Errorf("privileged exec allowed: %d", code)
	}
}

// Each mutation turns a valid sandbox into one that could reach the host. The proxy must refuse
// and nothing may be created (the proxy may read the DNS volume's location from the daemon to check
// the resolver bind mount, which creates nothing).
func TestDangerousContainerCreatesAreRefused(t *testing.T) {
	cases := map[string]func(b map[string]any){
		"privileged":        func(b map[string]any) { hc(b)["Privileged"] = true },
		"runc runtime":      func(b map[string]any) { hc(b)["Runtime"] = "runc" },
		"no runtime":        func(b map[string]any) { delete(hc(b), "Runtime") },
		"host bind":         func(b map[string]any) { hc(b)["Binds"] = []string{"/:/host"} },
		"host bind mount":   func(b map[string]any) { mounts(b)[1]["Source"] = "/etc" },
		"writable dns bind": func(b map[string]any) { mounts(b)[1]["ReadOnly"] = false },
		"foreign volume":    func(b map[string]any) { mounts(b)[0]["Source"] = "postgres-data" },
		"host network":      func(b map[string]any) { hc(b)["NetworkMode"] = "host" },
		"container network": func(b map[string]any) { hc(b)["NetworkMode"] = "container:abc" },
		"host pid":          func(b map[string]any) { hc(b)["PidMode"] = "host" },
		"host userns":       func(b map[string]any) { hc(b)["UsernsMode"] = "host" },
		"cap add":           func(b map[string]any) { hc(b)["CapAdd"] = []string{"SYS_ADMIN"} },
		"caps kept":         func(b map[string]any) { delete(hc(b), "CapDrop") },
		"devices":           func(b map[string]any) { hc(b)["Devices"] = []map[string]string{{"PathOnHost": "/dev/sda"}} },
		"unconfined": func(b map[string]any) {
			hc(b)["SecurityOpt"] = []string{"no-new-privileges:true", "seccomp=unconfined"}
		},
		"no nnp":          func(b map[string]any) { hc(b)["SecurityOpt"] = []string{} },
		"root user":       func(b map[string]any) { b["User"] = "0" },
		"writable rootfs": func(b map[string]any) { hc(b)["ReadonlyRootfs"] = false },
		"no pids limit":   func(b map[string]any) { delete(hc(b), "PidsLimit") },
		"published port": func(b map[string]any) {
			hc(b)["PortBindings"] = map[string]any{"22/tcp": []map[string]string{{"HostPort": "2222"}}}
		},
		"sysctl":     func(b map[string]any) { hc(b)["Sysctls"] = map[string]string{"net.ipv4.ip_forward": "1"} },
		"unlabelled": func(b map[string]any) { b["Labels"] = map[string]string{"io.syntaxis.test": "x"} },
		"other network join": func(b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"bridge": map[string]any{}}}
		},
		"volume subpath": func(b map[string]any) { mounts(b)[0]["VolumeOptions"] = map[string]any{"Subpath": "../../"} },
	}
	f := newFx(t)
	for name, mutate := range cases {
		b := f.sandboxBody()
		mutate(b)
		f.refused(name, "POST", "/containers/create", b)
	}
	if out := docker(t, "ps", "-aq", "--filter", "label=io.syntaxis.test="+f.id); out != "" {
		t.Fatalf("a refused request still created containers: %s", out)
	}
}

func hc(b map[string]any) map[string]any { return b["HostConfig"].(map[string]any) }
func mounts(b map[string]any) []map[string]any {
	return hc(b)["Mounts"].([]map[string]any)
}

func TestEndpointsOutsideTheAllowListAreRefused(t *testing.T) {
	f := newFx(t)
	sbx := f.container(map[string]string{"io.syntaxis.managed": "true"}, "--runtime", "runsc", image, "sleep", "infinity")
	dials := f.dials.Load()
	for _, c := range [][2]string{
		{"POST", "/build"}, {"POST", "/v1.47/containers/" + sbx + "/commit"}, {"POST", "/images/create?fromImage=alpine"}, {"POST", "/swarm/init"},
		{"POST", "/plugins/pull"}, {"GET", "/containers/" + sbx + "/export"}, {"POST", "/v1.47/containers/../build"},
		{"POST", "/secrets/create"}, {"POST", "/containers/" + sbx + "/kill"},
	} {
		f.refused(c[0]+" "+c[1], c[0], c[1], map[string]any{})
	}
	if f.dials.Load() != dials {
		t.Fatal("the daemon was contacted for a refused endpoint")
	}
	if out, _ := exec.Command("docker", "info", "--format", "{{.Swarm.LocalNodeState}}").Output(); strings.TrimSpace(string(out)) != "inactive" {
		t.Fatalf("swarm state: %s", out)
	}
}

func TestContainerOperationsOnlyOnSyntaxisContainers(t *testing.T) {
	f := newFx(t)
	sbx := f.container(map[string]string{"io.syntaxis.managed": "true"}, "--runtime", "runsc", image, "sleep", "infinity")
	runc := f.container(map[string]string{"io.syntaxis.managed": "true"}, image, "sleep", "infinity")
	helper := f.container(map[string]string{"io.syntaxis.helper": "true"}, "-v", DNSVolume+":/vol", image, "true")
	other := f.container(nil, image, "sleep", "infinity")
	docker(t, "start", other)

	if code, b := f.do("POST", "/containers/"+sbx+"/start", nil); code != 204 {
		t.Fatalf("start sandbox: %d %s", code, b)
	}
	f.refused("a non-gVisor container may not start", "POST", "/containers/"+runc+"/start", nil)
	if f.running(runc) {
		t.Error("the runc container is running")
	}
	f.refused("the volume helper may never run", "POST", "/containers/"+helper+"/start", nil)
	if f.running(helper) {
		t.Error("the helper is running")
	}
	f.refused("exec into a foreign container", "POST", "/containers/"+other+"/exec", map[string]any{"Cmd": []string{"id"}})
	f.refused("removing a foreign container", "DELETE", "/containers/"+other, nil)
	if !f.exists("container", other) {
		t.Error("the foreign container was removed")
	}
	f.refused("reading a foreign container's logs", "GET", "/containers/"+other+"/logs", nil)
	f.refused("copying files into a sandbox", "PUT", "/containers/"+sbx+"/archive?path=/workspace", nil)

	// an exec that really belongs to the foreign container cannot be started through the proxy
	code, b := raw(t, "POST", "/containers/"+other+"/exec", map[string]any{"Cmd": []string{"id"}})
	var ex struct{ Id string }
	_ = json.Unmarshal(b, &ex)
	if code != 201 || ex.Id == "" {
		t.Fatalf("setup: %d %s", code, b)
	}
	f.refused("starting an exec in a foreign container", "POST", "/exec/"+ex.Id+"/start", map[string]any{"Detach": true})

	// the resolver file may be written through the helper (what the manager does), and nothing else
	if code, _ := f.do("PUT", "/containers/"+helper+"/archive?path=/vol", nil); code == http.StatusForbidden {
		t.Errorf("writing the resolver file through the helper was refused")
	}
}

func TestVolumeAndNetworkRules(t *testing.T) {
	f := newFx(t)
	vol := "syntaxis-ws-u" + f.userID()
	if code, b := f.do("POST", "/volumes/create", map[string]any{"Name": vol}); code != 201 {
		t.Fatalf("workspace volume: %d %s", code, b)
	}
	if !f.exists("volume", vol) {
		t.Fatal("the volume was not created")
	}
	for name, body := range map[string]map[string]any{
		"host path volume": {"Name": "syntaxis-x" + f.id, "DriverOpts": map[string]string{"type": "none", "o": "bind", "device": "/"}},
		"foreign name":     {"Name": "data" + f.id},
	} {
		f.refused(name, "POST", "/volumes/create", body)
	}
	if f.exists("volume", "syntaxis-x"+f.id) || f.exists("volume", "data"+f.id) {
		t.Fatal("a refused volume was created")
	}
	foreign := "zz-foreign-" + f.id
	docker(t, "volume", "create", foreign)
	t.Cleanup(func() { dockerQuiet("volume", "rm", "-f", foreign) })
	f.refused("deleting a foreign volume", "DELETE", "/volumes/"+foreign, nil)
	if !f.exists("volume", foreign) {
		t.Error("the foreign volume was deleted")
	}
	// the sandbox network is the only one that may be created
	f.refused("other network", "POST", "/networks/create", map[string]any{"Name": "evil" + f.id, "Driver": "macvlan"})
	if f.exists("network", "evil"+f.id) {
		t.Fatal("a refused network was created")
	}
}

func TestContainerListShowsOnlySyntaxisContainers(t *testing.T) {
	f := newFx(t)
	mine := f.container(map[string]string{"io.syntaxis.managed": "true"}, image, "true")
	other := f.container(nil, image, "true")
	for name, query := range map[string]string{
		"no filters":       "all=1",
		"other label":      "all=1&filters=" + `{"label":["io.syntaxis.test=` + f.id + `"]}`,
		"old filter form":  "all=1&filters=" + `{"label":{"io.syntaxis.test=` + f.id + `":true}}`,
		"already asks for": "all=1&filters=" + `{"label":["io.syntaxis.managed=true"]}`,
		"tries to widen":   "all=1&filters=" + `{"name":["` + strings.TrimPrefix(docker(t, "inspect", "-f", "{{.Name}}", other), "/") + `"]}`,
	} {
		code, b := f.do("GET", "/containers/json?"+strings.ReplaceAll(strings.ReplaceAll(query, `"`, "%22"), " ", ""), nil)
		if code != 200 {
			t.Errorf("%s: %d %s", name, code, b)
			continue
		}
		var list []struct{ Id string }
		_ = json.Unmarshal(b, &list)
		sawMine := false
		for _, c := range list {
			if c.Id == other {
				t.Errorf("%s: the list shows a container that is not Syntaxis's", name)
			}
			if c.Id == mine {
				sawMine = true
			}
		}
		if name != "tries to widen" && !sawMine {
			t.Errorf("%s: the list does not show the Syntaxis container", name)
		}
	}
	f.refused("malformed filters", "GET", "/containers/json?filters=not-json", nil)
}
