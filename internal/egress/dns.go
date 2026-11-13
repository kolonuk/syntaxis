// SPDX-License-Identifier: GPL-2.0-or-later

package egress

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DNSLogEntry is one resolved or refused query, for the egress log.
type DNSLogEntry struct {
	Time    time.Time
	Source  netip.Addr
	Name    string
	Allowed bool
	Reason  string
	Answers []netip.Addr
}

// Resolver is the Syntaxis-run DNS forwarder on the sandbox network. It
// enforces per-sandbox domain policy, refuses answers that point into blocked
// ranges (DNS rebinding to the LAN or metadata endpoints) and logs queries.
// Payloads are never captured: only names and answer addresses.
type Resolver struct {
	// Upstream is the real resolver (host:port).
	Upstream string
	// PolicyFor maps a sandbox source address to its policy. ok=false drops the query.
	PolicyFor func(src netip.Addr) (Policy, bool)
	// Log receives one entry per query.
	Log func(DNSLogEntry)
	// OnAnswer lets the nft allowed_hosts set be fed in allowlist mode.
	OnAnswer func(src netip.Addr, name string, ips []netip.Addr)

	mu   sync.Mutex
	conn net.PacketConn
}

// Serve answers queries on addr (UDP) until ctx is cancelled.
func (r *Resolver) Serve(ctx context.Context, addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.conn = pc
	r.mu.Unlock()
	go func() { <-ctx.Done(); _ = pc.Close() }()
	buf := make([]byte, 4096)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		pkt := append([]byte(nil), buf[:n]...)
		go func() {
			if resp := r.Handle(pkt, from); resp != nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}()
	}
}

// Addr returns the bound address (after Serve started).
func (r *Resolver) Addr() net.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return nil
	}
	return r.conn.LocalAddr()
}

// Handle processes one query packet and returns the response (nil = drop).
func (r *Resolver) Handle(pkt []byte, from net.Addr) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(pkt)
	if err != nil {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	ua, ok := from.(*net.UDPAddr)
	if !ok {
		return nil
	}
	src, _ := netip.AddrFromSlice(ua.IP)
	src = src.Unmap()
	pol, known := r.PolicyFor(src)
	if !known {
		return nil // not one of our sandboxes
	}
	name := q.Name.String()
	entry := DNSLogEntry{Time: time.Now(), Source: src, Name: name}
	if d := pol.DNSDecision(name); !d.Allowed {
		entry.Reason = d.Reason
		r.log(entry)
		return refuse(h, q)
	}
	resp, err := r.forward(pkt)
	if err != nil {
		entry.Reason = "upstream error"
		r.log(entry)
		return refuse(h, q)
	}
	ips, ok := answerIPs(resp)
	if !ok {
		return nil
	}
	for _, ip := range ips {
		if blocked, why := IsAlwaysBlocked(ip); blocked {
			entry.Reason = "answer points into a blocked range (" + why + ")"
			r.log(entry)
			return refuse(h, q)
		}
		for _, x := range pol.ExtraBlocked {
			if x.Contains(ip) {
				entry.Reason = "answer points at a Syntaxis-internal address"
				r.log(entry)
				return refuse(h, q)
			}
		}
	}
	entry.Allowed, entry.Answers, entry.Reason = true, ips, "resolved"
	r.log(entry)
	if r.OnAnswer != nil && len(ips) > 0 {
		r.OnAnswer(src, name, ips)
	}
	return resp
}

func (r *Resolver) log(e DNSLogEntry) {
	if r.Log != nil {
		r.Log(e)
	}
}

func (r *Resolver) forward(pkt []byte) ([]byte, error) {
	c, err := net.DialTimeout("udp", r.Upstream, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write(pkt); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func answerIPs(resp []byte) ([]netip.Addr, bool) {
	var p dnsmessage.Parser
	if _, err := p.Start(resp); err != nil {
		return nil, false
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, false
	}
	var ips []netip.Addr
	for {
		rh, err := p.AnswerHeader()
		if err != nil {
			break
		}
		switch rh.Type {
		case dnsmessage.TypeA:
			a, err := p.AResource()
			if err != nil {
				return nil, false
			}
			ips = append(ips, netip.AddrFrom4(a.A))
		case dnsmessage.TypeAAAA:
			a, err := p.AAAAResource()
			if err != nil {
				return nil, false
			}
			ips = append(ips, netip.AddrFrom16(a.AAAA))
		default:
			if err := p.SkipAnswer(); err != nil {
				return nil, false
			}
		}
	}
	return ips, true
}

func refuse(h dnsmessage.Header, q dnsmessage.Question) []byte {
	m := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeRefused, RecursionAvailable: true},
		Questions: []dnsmessage.Question{q},
	}
	b, err := m.Pack()
	if err != nil {
		return nil
	}
	return b
}
