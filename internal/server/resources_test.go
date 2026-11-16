// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

func TestFullWorkspaceBlocksNewWorkUntilSpaceIsFreed(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fake := sandbox.NewFake()
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return &stubProvider{}, nil }
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	u := e.userSession("a@example.com")
	uid := userID(t, e, "a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)

	// the user's sandbox is running and its workspace is over the limit (5 GiB by default)
	cid, _ := fake.Create(context.Background(), sandbox.Spec{Labels: map[string]string{sandbox.LabelManaged: "true", sandbox.LabelUser: strconv.FormatInt(uid, 10), sandbox.LabelSession: id}})
	_ = fake.Start(context.Background(), cid)
	var size atomic.Int64
	size.Store(6 << 30)
	mon := sandbox.NewMonitor(fake, sandbox.NewWatchdog(sandbox.DefaultWatchdogConfig()), 2, nil)
	mon.DiskBytes = func(context.Context, string) (int64, error) { return size.Load(), nil }
	e.app.Monitor = mon
	mon.Tick(context.Background(), time.Now())

	code, m, _ := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "hello"})
	if code != 507 || !strings.Contains(m["error"].(string), "workspace is full") {
		t.Fatalf("a full workspace must refuse new work with an explanation: %d %v", code, m)
	}
	// the terminal is the way out: it must not be blocked by the same check
	if code, body, _ := u.do("GET", "/api/sessions/"+id+"/terminal", nil); code == 507 {
		t.Fatalf("the terminal must stay available to free space: %v", body)
	}
	// the user deletes files; the next message re-measures and goes through at once
	size.Store(1 << 30)
	if code, _, raw := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "hello"}); code != 202 {
		t.Fatalf("after freeing space: %d %s", code, raw)
	}
}
