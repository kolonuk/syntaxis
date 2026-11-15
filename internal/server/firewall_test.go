// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/egress"
)

func fwReq(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestFirewallEndpointsAreOffWithoutATokenAndGuardedWithOne(t *testing.T) {
	e := newEnv(t)
	if code, _ := fwReq(t, "GET", e.admin.URL+"/internal/firewall", "", ""); code != 404 {
		t.Fatalf("no token configured: %d", code)
	}
	e.app.Cfg.FirewallToken = "s3cret-token-value"
	e.app.FirewallState = func(context.Context) ([]egress.SandboxEntry, []egress.HostEntry) {
		return []egress.SandboxEntry{{IP: netip.MustParseAddr("172.30.0.5"), User: 7, Session: "sess", Mode: egress.ModeAllowlist}},
			[]egress.HostEntry{{IP: netip.MustParseAddr("93.184.216.34"), TTL: 2 * time.Minute}}
	}
	for _, tok := range []string{"", "wrong", "s3cret-token-valu"} {
		if code, _ := fwReq(t, "GET", e.admin.URL+"/internal/firewall", tok, ""); code != 401 {
			t.Fatalf("token %q: %d", tok, code)
		}
	}
	code, body := fwReq(t, "GET", e.admin.URL+"/internal/firewall", "s3cret-token-value", "")
	if code != 200 || body != "sandbox 172.30.0.5 allow\nhost 93.184.216.34 120\n" {
		t.Fatalf("%d %q", code, body)
	}
	if e.app.FirewallSyncAge() > time.Second {
		t.Fatal("a fetch must be remembered, for the health check")
	}
	// the chat app never serves these
	if code, _ := fwReq(t, "GET", e.user.URL+"/internal/firewall", "s3cret-token-value", ""); code == 200 {
		t.Fatal("the internal endpoint must only exist on the admin listener")
	}
}

func TestFirewallLogIsStoredForTheOwningUserOnly(t *testing.T) {
	e := newEnv(t)
	e.app.Cfg.FirewallToken = "tok"
	e.app.FirewallState = func(context.Context) ([]egress.SandboxEntry, []egress.HostEntry) {
		return []egress.SandboxEntry{{IP: netip.MustParseAddr("172.30.0.5"), User: 7, Session: "sess"}}, nil
	}
	lines := "syxa: IN=br0 SRC=172.30.0.5 DST=93.184.216.34 PROTO=TCP SPT=1 DPT=443\n" +
		"syxd: IN=br0 SRC=172.30.0.5 DST=169.254.169.254 PROTO=TCP SPT=2 DPT=80\n" +
		"syxa: IN=br0 SRC=172.30.0.99 DST=1.1.1.1 PROTO=TCP SPT=3 DPT=443\n" + // not one of ours
		"garbage line\n"
	if code, body := fwReq(t, "POST", e.admin.URL+"/internal/firewall/log", "tok", lines); code != 200 || !strings.Contains(body, `"recorded":2`) {
		t.Fatalf("%d %s", code, body)
	}
	got, _ := e.st.ListEgress(context.Background(), 7, 10)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	for _, g := range got {
		if g.SessionID != "sess" || g.DNS || (g.DestIP == "169.254.169.254") == g.Allowed {
			t.Fatalf("%+v", g)
		}
	}
	if other, _ := e.st.ListEgress(context.Background(), 8, 10); len(other) != 0 {
		t.Fatal("another user's log must stay empty")
	}
}
