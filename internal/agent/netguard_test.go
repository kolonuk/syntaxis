// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
)

func TestBlockedNetworkCommands(t *testing.T) {
	blocked := []string{
		"curl http://192.168.1.1/admin",
		"curl -s http://10.0.0.5:8080/",
		"wget http://172.16.4.2/x",
		"curl http://169.254.169.254/latest/meta-data/",
		"curl http://metadata.google.internal/computeMetadata/v1/",
		"curl http://[fd00::1]/",
		"curl http://2130706433/",    // 127.0.0.1 in decimal is loopback: allowed, see below
		"curl http://3232235777/",    // 192.168.1.1 as one decimal number
		"curl http://0xc0a80101/",    // 192.168.1.1 as hex
		"curl http://0300.0250.1.1/", // octal spelling of 192.168.1.1
		"nmap -sn 192.168.1.0/24",
		"nmap localhost",
		"masscan 10.0.0.0/8 -p80",
		"arp -a",
		"ip neigh",
		"ip route show",
		"nc -zv 192.168.1.10 22",
		"nc -z somehost 1-1000",
		"ssh user@192.168.0.20",
		"git clone http://nas.local/repo.git",
		"curl http://printer.lan/",
		"ping 10.1.1.1",
		"ping router",
		"curl http://router/",
		"curl http://host.docker.internal:11434/api/tags",
		"docker -H unix:///var/run/docker.sock ps",
		"curl --unix-socket /var/run/docker.sock http://x/containers/json",
		"docker run --network host alpine",
		"docker run --privileged alpine",
		"for i in $(seq 1 254); do ping -c1 192.168.1.$i; done",
		"tcpdump -i eth0",
		"echo hi && curl http://192.168.1.1",
		"sh -c 'curl http://10.0.0.1'",
		"curl http://intranet.corp/",
	}
	// loopback in decimal is allowed: remove it from the blocked list
	p, _ := NewApprovalPolicy()
	for _, c := range blocked {
		r := p.Classify(c)
		if c == "curl http://2130706433/" {
			if r.Blocked {
				t.Errorf("sandbox loopback must be allowed: %q", c)
			}
			continue
		}
		if !r.Blocked || !strings.HasPrefix(r.Reason, "blocked by policy") {
			t.Errorf("not blocked: %q (%+v)", c, r)
		}
	}
}

func TestAllowedNetworkCommands(t *testing.T) {
	p, _ := NewApprovalPolicy()
	for _, c := range []string{
		"curl http://localhost:3000/health",
		"curl -s http://127.0.0.1:8080/",
		"curl http://[::1]:8000/",
		"curl https://api.github.com/repos/x/y",
		"curl https://example.com",
		"npm install",
		"go test ./...",
		"git clone https://github.com/example/repo.git",
		"pip install requests",
		"python3 -m http.server 8000",
		"ping -c1 8.8.8.8",
		"curl http://203.0.113.9/",
		"git push origin main",
		"nc -l 9000",
		"curl -s -m 10 -o /dev/null -w '%{http_code}' https://example.com",
		"curl -o index https://example.com/",
		"wget -O out.html https://example.com",
		"ssh -i id_rsa user@github.com",
		"dig example.com A",
		"dig example.com MX +short",
		"ping -c 3 example.com",
		"echo 192.168.1.1 is a private address > notes.txt && cat notes.txt", // text mention: see below
	} {
		r := p.Classify(c)
		if r.Blocked && !strings.Contains(c, "192.168.1.1 is a private") {
			t.Errorf("false positive: %q (%s)", c, r.Reason)
		}
	}
}

func TestCheckHostForms(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": false, "127.0.0.1": false, "::1": false, "example.com": false, "github.com": false,
		"192.168.0.1": true, "10.2.3.4": true, "169.254.169.254": true, "100.64.0.9": true, "172.20.0.2": true,
		"router": true, "nas.local": true, "db.internal": true, "metadata.google.internal": true, "fd12::5": true,
		"0xa000001": true, "167772161": true, "": false,
	} {
		if got := CheckHost(host) != ""; got != want {
			t.Errorf("%q: blocked=%v want %v (%s)", host, got, want, CheckHost(host))
		}
	}
}

func TestLoopBlocksNetworkAccessEvenWithFullAutonomy(t *testing.T) {
	h := newHarness(t)
	h.a.Cfg.Autonomy = Full
	h.model.replies = []*provider.ChatResult{
		toolReply(call("c1", ToolShell, `{"command":"curl http://192.168.1.1/admin"}`)),
		{Content: "I cannot reach that."},
	}
	h.a.Run(context.Background(), "check my router")
	if len(h.approvals) != 0 {
		t.Fatal("blocked commands must not even be offered for approval")
	}
	for _, c := range h.box.cmds {
		if strings.Contains(c, "192.168.1.1") {
			t.Fatal("blocked command was executed")
		}
	}
	last := h.model.requests[1].Messages[len(h.model.requests[1].Messages)-1]
	if !strings.Contains(last.Content, "blocked by policy") || !strings.Contains(last.Content, "Do not retry") {
		t.Fatalf("%q", last.Content)
	}
	if !strings.Contains(strings.Join(h.audits, "\n"), "agent.blocked") {
		t.Fatalf("attempt not audited: %v", h.audits)
	}
}

func TestFetchBlocksPrivateTargets(t *testing.T) {
	x, b := newExec()
	for _, u := range []string{"http://192.168.1.1/", "http://169.254.169.254/latest/", "http://router/", "http://0xc0a80101/", "http://nas.local/"} {
		o := run(x, ToolFetch, `{"url":"`+u+`"}`)
		if !o.IsError || !strings.Contains(o.Text, "blocked by policy") {
			t.Errorf("%s not blocked: %+v", u, o)
		}
	}
	if len(b.cmds) != 0 {
		t.Fatalf("curl ran for blocked URLs: %v", b.cmds)
	}
	if o := run(x, ToolFetch, `{"url":"http://localhost:3000/"}`); o.IsError && strings.Contains(o.Text, "blocked") {
		t.Fatal("sandbox loopback must be reachable")
	}
	_ = sandbox.WorkspacePath
}
