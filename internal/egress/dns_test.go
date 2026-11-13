// SPDX-License-Identifier: GPL-2.0-or-later

package egress

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeUpstream answers A queries from a table.
func fakeUpstream(t *testing.T, table map[string]string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback UDP: " + err.Error())
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			h, _ := p.Start(buf[:n])
			q, _ := p.Question()
			m := dnsmessage.Message{Header: dnsmessage.Header{ID: h.ID, Response: true}, Questions: []dnsmessage.Question{q}}
			if ip, ok := table[q.Name.String()]; ok {
				a := netip.MustParseAddr(ip).As4()
				m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, Body: &dnsmessage.AResource{A: a}}}
			}
			out, _ := m.Pack()
			_, _ = pc.WriteTo(out, from)
		}
	}()
	return pc.LocalAddr().String()
}

func query(t *testing.T, srv, name string) dnsmessage.Message {
	t.Helper()
	n, _ := dnsmessage.NewName(name)
	m := dnsmessage.Message{Header: dnsmessage.Header{ID: 7, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	b, _ := m.Pack()
	c, err := net.Dial("udp", srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = c.Write(b)
	buf := make([]byte, 1500)
	k, err := c.Read(buf)
	if err != nil {
		t.Fatalf("no response: %v", err)
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:k]); err != nil {
		t.Fatal(err)
	}
	return resp
}

func startResolver(t *testing.T, pol Policy, table map[string]string) (string, *[]DNSLogEntry, *sync.Mutex) {
	t.Helper()
	up := fakeUpstream(t, table)
	var mu sync.Mutex
	var logs []DNSLogEntry
	r := &Resolver{Upstream: up,
		PolicyFor: func(src netip.Addr) (Policy, bool) { return pol, src.IsLoopback() },
		Log:       func(e DNSLogEntry) { mu.Lock(); logs = append(logs, e); mu.Unlock() }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = r.Serve(ctx, "127.0.0.1:0") }()
	for i := 0; i < 100 && r.Addr() == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if r.Addr() == nil {
		t.Fatal("resolver did not start")
	}
	return r.Addr().String(), &logs, &mu
}

func TestResolverForwardsAndLogs(t *testing.T) {
	srv, logs, mu := startResolver(t, Policy{Mode: ModeOpen}, map[string]string{"example.com.": "93.184.216.34"})
	resp := query(t, srv, "example.com.")
	if resp.RCode != dnsmessage.RCodeSuccess || len(resp.Answers) != 1 {
		t.Fatalf("%+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*logs) != 1 || !(*logs)[0].Allowed || (*logs)[0].Name != "example.com." || (*logs)[0].Answers[0].String() != "93.184.216.34" {
		t.Fatalf("%+v", *logs)
	}
}

func TestResolverBlocksRebindingToPrivateRanges(t *testing.T) {
	for name, ip := range map[string]string{"lan.evil.test.": "192.168.1.10", "meta.evil.test.": "169.254.169.254", "local.evil.test.": "127.0.0.1", "docker.evil.test.": "172.17.0.1"} {
		srv, logs, mu := startResolver(t, Policy{Mode: ModeOpen}, map[string]string{name: ip})
		resp := query(t, srv, name)
		if resp.RCode != dnsmessage.RCodeRefused || len(resp.Answers) != 0 {
			t.Errorf("%s -> %s resolved for the sandbox: %+v", name, ip, resp)
		}
		mu.Lock()
		if len(*logs) != 1 || (*logs)[0].Allowed {
			t.Errorf("blocked answer not logged as refused: %+v", *logs)
		}
		mu.Unlock()
	}
}

func TestResolverBlocksInternalAddresses(t *testing.T) {
	pol := Policy{Mode: ModeOpen, ExtraBlocked: []netip.Prefix{netip.MustParsePrefix("203.0.113.5/32")}}
	srv, _, _ := startResolver(t, pol, map[string]string{"llm.example.test.": "203.0.113.5"})
	if resp := query(t, srv, "llm.example.test."); resp.RCode != dnsmessage.RCodeRefused {
		t.Fatal("the LLM server's address resolved for a sandbox")
	}
}

func TestResolverAllowlistAndNone(t *testing.T) {
	table := map[string]string{"github.com.": "140.82.112.3", "evil.test.": "6.6.6.6"}
	srv, logs, mu := startResolver(t, Policy{Mode: ModeAllowlist, Allowlist: []string{"github.com"}}, table)
	if resp := query(t, srv, "github.com."); resp.RCode != dnsmessage.RCodeSuccess || len(resp.Answers) != 1 {
		t.Fatal("allowlisted name refused")
	}
	if resp := query(t, srv, "evil.test."); resp.RCode != dnsmessage.RCodeRefused {
		t.Fatal("unlisted name resolved in allowlist mode")
	}
	mu.Lock()
	if len(*logs) != 2 || (*logs)[1].Allowed {
		t.Fatalf("%+v", *logs)
	}
	mu.Unlock()
	srv2, _, _ := startResolver(t, Policy{Mode: ModeNone}, table)
	if resp := query(t, srv2, "github.com."); resp.RCode != dnsmessage.RCodeRefused {
		t.Fatal("none mode resolved a name")
	}
}

func TestResolverIgnoresUnknownSources(t *testing.T) {
	up := fakeUpstream(t, nil)
	r := &Resolver{Upstream: up, PolicyFor: func(netip.Addr) (Policy, bool) { return Policy{}, false }}
	n, _ := dnsmessage.NewName("a.test.")
	m := dnsmessage.Message{Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	b, _ := m.Pack()
	if resp := r.Handle(b, &net.UDPAddr{IP: net.ParseIP("10.9.9.9"), Port: 1}); resp != nil {
		t.Fatal("answered a source that is not a sandbox")
	}
	if r.Handle([]byte("garbage"), &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}) != nil {
		t.Fatal("garbage answered")
	}
}
