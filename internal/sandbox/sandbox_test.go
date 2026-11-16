// SPDX-License-Identifier: GPL-2.0-or-later

package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildSpecIsolationProperties(t *testing.T) {
	s, err := BuildSpec(DefaultConfig(), 7, "sess-1", map[string]string{"GH_TOKEN": "x"}, false)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]bool{
		"runtime is runsc":        s.Runtime == "runsc",
		"not privileged":          !s.Privileged,
		"read-only rootfs":        s.ReadOnlyRootFS,
		"cap_drop ALL":            len(s.CapDrop) == 1 && s.CapDrop[0] == "ALL",
		"no cap_add":              len(s.CapAdd) == 0,
		"no-new-privileges":       len(s.SecurityOpt) == 1 && s.SecurityOpt[0] == "no-new-privileges:true",
		"non-root user":           s.User == "10001:10001",
		"pids limit":              s.PidsLimit > 0,
		"memory limit":            s.MemoryBytes > 0,
		"no swap":                 s.MemorySwap == s.MemoryBytes,
		"cpu limit":               s.NanoCPUs > 0,
		"init reaps zombies":      s.Init,
		"exactly one mount":       len(s.Mounts) == 1,
		"workspace is user's vol": s.Mounts[0].Volume == "syntaxis-ws-u7" && s.Mounts[0].Target == "/workspace",
		"tmpfs /tmp nosuid":       strings.Contains(s.Tmpfs["/tmp"], "nosuid") && !strings.Contains(s.Tmpfs["/tmp"], "noexec"),
		"managed label":           s.Labels[LabelManaged] == "true" && s.Labels[LabelUser] == "7" && s.Labels[LabelSession] == "sess-1",
		"env passthrough":         contains(s.Env, "GH_TOKEN=x"),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("spec property failed: %s\n%+v", name, s)
		}
	}
	for _, e := range s.Env {
		if strings.Contains(strings.ToUpper(e), "DOCKER_HOST") || strings.Contains(e, "OLLAMA") {
			t.Errorf("forbidden env leaked: %s", e)
		}
	}
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func TestValidateRejectsEachViolation(t *testing.T) {
	good, _ := BuildSpec(DefaultConfig(), 1, "s", nil, false)
	mutations := map[string]func(*Spec){
		"runc runtime":    func(s *Spec) { s.Runtime = "runc" },
		"empty runtime":   func(s *Spec) { s.Runtime = "" },
		"privileged":      func(s *Spec) { s.Privileged = true },
		"writable rootfs": func(s *Spec) { s.ReadOnlyRootFS = false },
		"cap_add":         func(s *Spec) { s.CapAdd = []string{"SYS_ADMIN"} },
		"keep caps":       func(s *Spec) { s.CapDrop = nil },
		"no nnp":          func(s *Spec) { s.SecurityOpt = nil },
		"seccomp off":     func(s *Spec) { s.SecurityOpt = []string{"no-new-privileges:true", "seccomp=unconfined"} },
		"root user":       func(s *Spec) { s.User = "root" },
		"uid 0":           func(s *Spec) { s.User = "0:0" },
		"empty user":      func(s *Spec) { s.User = "" },
		"no pids":         func(s *Spec) { s.PidsLimit = 0 },
		"no memory":       func(s *Spec) { s.MemoryBytes = 0 },
		"no cpu":          func(s *Spec) { s.NanoCPUs = 0 },
		"host bind":       func(s *Spec) { s.Mounts = append(s.Mounts, Mount{Volume: "/etc", Target: "/host"}) },
		"docker socket": func(s *Spec) {
			s.Mounts = append(s.Mounts, Mount{Volume: "/var/run/docker.sock", Target: "/var/run/docker.sock"})
		},
		"other user's vol":  func(s *Spec) { s.Mounts[0].Volume = WorkspaceVolume(2) },
		"docker_host env":   func(s *Spec) { s.Env = append(s.Env, "DOCKER_HOST=tcp://x:2375") },
		"label tampering":   func(s *Spec) { s.Labels[LabelUser] = "99" },
		"managed label off": func(s *Spec) { s.Labels[LabelManaged] = "false" },
	}
	for name, mut := range mutations {
		s := good
		s.Mounts = append([]Mount(nil), good.Mounts...)
		s.Labels = map[string]string{}
		for k, v := range good.Labels {
			s.Labels[k] = v
		}
		mut(&s)
		if err := Validate(s, 1); !errors.Is(err, ErrBadSpec) {
			t.Errorf("%s: not rejected (%v)", name, err)
		}
	}
	if err := Validate(good, 1); err != nil {
		t.Fatalf("good spec rejected: %v", err)
	}
}

func TestBuildSpecNoRuntime(t *testing.T) {
	c := DefaultConfig()
	c.Runtime = ""
	if _, err := BuildSpec(c, 1, "s", nil, false); err == nil {
		t.Fatal("spec built without gVisor runtime")
	}
	c.Runtime = "runc"
	if _, err := BuildSpec(c, 1, "s", nil, false); err == nil {
		t.Fatal("runc accepted")
	}
}

func TestContainerNameSanitised(t *testing.T) {
	if n := ContainerName("a/b; rm -rf"); strings.ContainsAny(n, "/; ") {
		t.Fatal(n)
	}
}

// The DNS mount is built into the spec read-only from a subpath of the shared volume (the embedded
// resolver does not work under gVisor); the real container using it is in integration_test.go.
func TestDNSMountIsReadOnlySubpathInTheSpec(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DNS = []string{"172.30.0.2", "1.1.1.1"}
	spec, err := BuildSpec(cfg, 1, "s", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var dnsMount *Mount
	for i, mn := range spec.Mounts {
		if mn.Target == "/etc/resolv.conf" {
			dnsMount = &spec.Mounts[i]
		}
	}
	if dnsMount == nil || !dnsMount.ReadOnly || dnsMount.Subpath != "resolv.conf" || dnsMount.Volume != DNSVolume {
		t.Fatalf("resolv.conf mount: %+v", dnsMount)
	}
	if err := Validate(spec, 1); err != nil {
		t.Fatalf("spec with the DNS mount must stay valid: %v", err)
	}
	// without resolvers nothing is mounted
	plain, _ := BuildSpec(DefaultConfig(), 1, "s2", nil, false)
	if len(plain.Mounts) != 1 {
		t.Fatalf("%+v", plain.Mounts)
	}
}

func TestDNSMountCannotBeAbused(t *testing.T) {
	s, _ := BuildSpec(Config{Runtime: "runsc", Image: "i", Network: "n", User: "10001:10001", Limits: DefaultLimits(), DNS: []string{"1.1.1.1"}}, 1, "s", nil, false)
	bad := s
	bad.Mounts = append([]Mount(nil), s.Mounts...)
	for i := range bad.Mounts {
		if bad.Mounts[i].Volume == DNSVolume {
			bad.Mounts[i].ReadOnly = false
		}
	}
	if Validate(bad, 1) == nil {
		t.Fatal("a writable DNS mount was accepted")
	}
	bad2 := s
	bad2.Mounts = append(append([]Mount(nil), s.Mounts...), Mount{Volume: DNSVolume, Target: "/etc", ReadOnly: true, Subpath: "resolv.conf"})
	if Validate(bad2, 1) == nil {
		t.Fatal("the DNS volume mounted elsewhere was accepted")
	}
}

func TestResolvConfRejectsGarbage(t *testing.T) {
	if _, err := ResolvConf([]string{"1.1.1.1; rm -rf /"}); err == nil {
		t.Fatal("invalid server accepted")
	}
	if c, err := ResolvConf([]string{"9.9.9.9"}); err != nil || c != "nameserver 9.9.9.9\n" {
		t.Fatal(c, err)
	}
}

func TestFirewallVerdictOnlyTrustsATimeout(t *testing.T) {
	for code, want := range map[int]bool{28: true, 7: false, 0: false, 52: false, 6: false} {
		if FirewallVerdict(code) != want {
			t.Errorf("curl exit %d: want %v", code, want)
		}
	}
}
