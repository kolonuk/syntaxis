// SPDX-License-Identifier: GPL-2.0-or-later

package dockerproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDaemon records forwarded requests and answers inspections from a table.
type fakeDaemon struct {
	mu         sync.Mutex
	forwarded  []string
	queries    map[string]string
	bodies     map[string][]byte
	containers map[string]string // id -> inspect JSON
	execs      map[string]string // exec id -> container id
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "GET" && r.URL.Path != "/containers/json" && strings.HasPrefix(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
		if c, ok := f.containers[id]; ok {
			_, _ = io.WriteString(w, c)
			return
		}
		http.NotFound(w, r)
		return
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/exec/") && strings.HasSuffix(r.URL.Path, "/json") && f.execs != nil:
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/exec/"), "/json")
		if c, ok := f.execs[id]; ok {
			_ = json.NewEncoder(w).Encode(map[string]string{"ContainerID": c})
			return
		}
	case r.Method == "GET" && r.URL.Path == "/volumes/"+DNSVolume:
		_, _ = io.WriteString(w, `{"Name":"syntaxis-dns","Mountpoint":"/var/lib/docker/volumes/syntaxis-dns/_data"}`)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.forwarded = append(f.forwarded, r.Method+" "+r.URL.Path)
	if f.queries == nil {
		f.queries = map[string]string{}
	}
	f.queries[r.Method+" "+r.URL.Path] = r.URL.RawQuery
	f.bodies[r.Method+" "+r.URL.Path] = b
	_, _ = io.WriteString(w, `{}`)
}

func setup(t *testing.T) (*fakeDaemon, *httptest.Server) {
	t.Helper()
	f := &fakeDaemon{bodies: map[string][]byte{}, containers: map[string]string{
		"sbx":    `{"Config":{"Labels":{"io.syntaxis.managed":"true"}},"HostConfig":{"Runtime":"runsc"},"State":{"Running":true}}`,
		"runc":   `{"Config":{"Labels":{"io.syntaxis.managed":"true"}},"HostConfig":{"Runtime":"runc"}}`,
		"helper": `{"Config":{"Labels":{"io.syntaxis.helper":"true"}},"HostConfig":{"Runtime":""},"Mounts":[{"Name":"syntaxis-dns"}]}`,
		"other":  `{"Config":{"Labels":{}},"HostConfig":{"Runtime":"runc"},"State":{"Running":true}}`,
	}, execs: map[string]string{"e1": "sbx", "e2": "other"}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	addr := up.Listener.Addr().String()
	p := New(Config{Network: "syntaxis-sandbox", Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}})
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return f, srv
}

func do(t *testing.T, srv *httptest.Server, method, path string, body any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// sandboxBody is what Syntaxis sends for a session sandbox.
func sandboxBody() map[string]any {
	return map[string]any{
		"Image": "syntaxis-sandbox:latest", "User": "10001:10001", "Cmd": []string{"sleep", "infinity"},
		"Labels": map[string]string{"io.syntaxis.managed": "true", "io.syntaxis.user": "1"},
		"HostConfig": map[string]any{
			"Runtime": "runsc", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"},
			"Memory": 1 << 30, "NanoCPUs": 1e9, "PidsLimit": 256, "NetworkMode": "syntaxis-sandbox",
			"Tmpfs": map[string]string{"/tmp": "rw"},
			"Mounts": []map[string]any{
				{"Type": "volume", "Source": "syntaxis-ws-u1", "Target": "/workspace"},
				{"Type": "bind", "Source": "/var/lib/docker/volumes/syntaxis-dns/_data/resolv.conf", "Target": "/etc/resolv.conf", "ReadOnly": true},
			},
		},
	}
}

func TestSandboxCreateIsForwardedReEncoded(t *testing.T) {
	f, srv := setup(t)
	if code := do(t, srv, "POST", "/v1.47/containers/create?name=syntaxis-s-x", sandboxBody()); code != 200 {
		t.Fatalf("a valid sandbox was refused: %d", code)
	}
	body := f.bodies["POST /v1.47/containers/create"]
	if !strings.Contains(string(body), `"Runtime":"runsc"`) {
		t.Fatalf("forwarded body: %s", body)
	}
}

// Each mutation turns a valid sandbox into one that could reach the host.
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
		"unlabelled": func(b map[string]any) { b["Labels"] = map[string]string{} },
		"other network join": func(b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"bridge": map[string]any{}}}
		},
		"volume subpath": func(b map[string]any) { mounts(b)[0]["VolumeOptions"] = map[string]any{"Subpath": "../../"} },
	}
	for name, mutate := range cases {
		f, srv := setup(t)
		b := sandboxBody()
		mutate(b)
		if code := do(t, srv, "POST", "/containers/create", b); code != 403 {
			t.Errorf("%s: got %d, want 403", name, code)
		}
		if len(f.forwarded) != 0 {
			t.Errorf("%s: forwarded to the daemon: %v", name, f.forwarded)
		}
	}
}

func hc(b map[string]any) map[string]any { return b["HostConfig"].(map[string]any) }
func mounts(b map[string]any) []map[string]any {
	return hc(b)["Mounts"].([]map[string]any)
}

func TestEndpointsOutsideTheAllowListAreRefused(t *testing.T) {
	f, srv := setup(t)
	for _, c := range [][2]string{
		{"POST", "/build"}, {"POST", "/v1.47/containers/sbx/commit"}, {"POST", "/images/create"}, {"POST", "/swarm/init"},
		{"POST", "/plugins/pull"}, {"GET", "/containers/sbx/export"}, {"POST", "/v1.47/containers/../build"},
		{"POST", "/secrets/create"}, {"POST", "/containers/sbx/kill"},
	} {
		if code := do(t, srv, c[0], c[1], map[string]any{}); code != 403 {
			t.Errorf("%s %s: got %d", c[0], c[1], code)
		}
	}
	if len(f.forwarded) != 0 {
		t.Fatalf("forwarded: %v", f.forwarded)
	}
}

func TestContainerOperationsOnlyOnSyntaxisContainers(t *testing.T) {
	f, srv := setup(t)
	if code := do(t, srv, "POST", "/containers/sbx/start", nil); code != 200 {
		t.Fatalf("start sandbox: %d", code)
	}
	for _, c := range [][3]string{
		{"POST", "/containers/runc/start", "a non-gVisor container may not start"},
		{"POST", "/containers/helper/start", "the volume helper may never run"},
		{"POST", "/containers/other/exec", "exec into a foreign container"},
		{"DELETE", "/containers/other", "removing a foreign container"},
		{"GET", "/containers/other/logs", "reading a foreign container's logs"},
		{"PUT", "/containers/sbx/archive?path=/workspace", "copying files into a sandbox"},
		{"POST", "/exec/e2/start", "starting an exec in a foreign container"},
	} {
		if code := do(t, srv, c[0], c[1], map[string]any{"Cmd": []string{"id"}}); code != 403 {
			t.Errorf("%s: got %d", c[2], code)
		}
	}
	if code := do(t, srv, "POST", "/containers/sbx/exec", map[string]any{"Cmd": []string{"id"}, "User": "root"}); code != 403 {
		t.Errorf("root exec allowed: %d", code)
	}
	if code := do(t, srv, "POST", "/containers/sbx/exec", map[string]any{"Cmd": []string{"id"}, "Privileged": true}); code != 403 {
		t.Errorf("privileged exec allowed: %d", code)
	}
	if code := do(t, srv, "POST", "/containers/sbx/exec", map[string]any{"Cmd": []string{"id"}}); code != 200 {
		t.Errorf("normal exec refused: %d", code)
	}
	if code := do(t, srv, "PUT", "/containers/helper/archive?path=/vol", nil); code != 200 {
		t.Errorf("writing the resolver file through the helper refused: %d", code)
	}
	if len(f.forwarded) != 3 {
		t.Fatalf("forwarded: %v", f.forwarded)
	}
}

func TestVolumeAndNetworkRules(t *testing.T) {
	_, srv := setup(t)
	if code := do(t, srv, "POST", "/volumes/create", map[string]any{"Name": "syntaxis-ws-u3"}); code != 200 {
		t.Fatalf("workspace volume: %d", code)
	}
	for name, body := range map[string]map[string]any{
		"host path volume": {"Name": "syntaxis-x", "DriverOpts": map[string]string{"type": "none", "o": "bind", "device": "/"}},
		"foreign name":     {"Name": "data"},
	} {
		if code := do(t, srv, "POST", "/volumes/create", body); code != 403 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code := do(t, srv, "DELETE", "/volumes/postgres-data", nil); code != 403 {
		t.Errorf("deleting a foreign volume: %d", code)
	}
	if code := do(t, srv, "POST", "/networks/create", map[string]any{"Name": "syntaxis-sandbox", "Driver": "bridge", "Options": map[string]string{"com.docker.network.bridge.enable_icc": "true"}}); code != 200 {
		t.Fatalf("sandbox network: %d", code)
	}
	if code := do(t, srv, "POST", "/networks/create", map[string]any{"Name": "evil", "Driver": "macvlan"}); code != 403 {
		t.Errorf("other network: %d", code)
	}
}

func TestContainerListShowsOnlySyntaxisContainers(t *testing.T) {
	for name, query := range map[string]string{
		"no filters":       "all=1",
		"other label":      "filters=" + `{"label":["app=web"]}`,
		"old filter form":  "filters=" + `{"label":{"app=web":true},"status":{"running":true}}`,
		"already asks for": "filters=" + `{"label":["io.syntaxis.managed=true"]}`,
		"tries to widen":   "filters=" + `{"name":["x"]}`,
	} {
		f, srv := setup(t)
		req, _ := http.NewRequest("GET", srv.URL+"/containers/json?"+query, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		raw := f.queries["GET /containers/json"]
		if !strings.Contains(raw, "io.syntaxis.managed%3Dtrue") {
			t.Errorf("%s: the daemon was asked for %q, which is not limited to Syntaxis containers", name, raw)
		}
		if strings.Count(raw, "io.syntaxis.managed") != 1 {
			t.Errorf("%s: label added twice: %q", name, raw)
		}
	}
	_, srv := setup(t)
	if code := do(t, srv, "GET", "/containers/json?filters=not-json", nil); code != http.StatusForbidden {
		t.Errorf("malformed filters must be refused, got %d", code)
	}
}
