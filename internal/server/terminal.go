// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// terminalIdle closes a terminal nobody has typed into for this long.
const terminalIdle = 15 * time.Minute

// Terminals tracks open interactive terminals, at most one per session.
type Terminals struct {
	mu sync.Mutex
	m  map[string]*terminal
}

type terminal struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	reason string
}

func (t *terminal) setReason(r string) { t.mu.Lock(); t.reason = r; t.mu.Unlock() }
func (t *terminal) why() string        { t.mu.Lock(); defer t.mu.Unlock(); return t.reason }

// NewTerminals returns an empty registry.
func NewTerminals() *Terminals { return &Terminals{m: map[string]*terminal{}} }

// CloseSession ends a session's terminal (if any) with a message for the user.
func (t *Terminals) CloseSession(id, reason string) {
	t.mu.Lock()
	cur := t.m[id]
	t.mu.Unlock()
	if cur != nil {
		cur.setReason(reason)
		cur.cancel()
	}
}

// Open reports whether the session has a terminal open.
func (t *Terminals) Open(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[id] != nil
}

func (t *Terminals) register(id string, cancel context.CancelFunc) *terminal {
	t.mu.Lock()
	prev := t.m[id]
	cur := &terminal{cancel: cancel}
	t.m[id] = cur
	t.mu.Unlock()
	if prev != nil { // a reconnect replaces the old connection
		prev.setReason("Opened somewhere else.")
		prev.cancel()
	}
	return cur
}

func (t *Terminals) remove(id string, cur *terminal) {
	t.mu.Lock()
	if t.m[id] == cur {
		delete(t.m, id)
	}
	t.mu.Unlock()
}

type termControl struct {
	Type string `json:"type"`
	Rows uint   `json:"rows"`
	Cols uint   `json:"cols"`
}

// handleTerminal upgrades to a WebSocket and connects it to a shell inside the
// session's own sandbox: the same container, user, limits and network policy the
// agent uses. Binary frames carry terminal bytes; text frames carry JSON controls.
func (a *App) handleTerminal(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	u := a.user(r)
	switch {
	case a.Sandbox == nil:
		writeErr(w, http.StatusServiceUnavailable, "Sandboxes are not available on this server.")
		return
	case u.TerminalDisabled:
		writeErr(w, http.StatusForbidden, "Your administrator has turned the terminal off for your account.")
		return
	}
	if run := a.Runs.Get(s.ID); run != nil && run.IsRunning() {
		writeErr(w, http.StatusConflict, "The agent is working in this sandbox. Stop it or wait until it finishes, then open the terminal.")
		return
	}
	if a.Quota.CPUExhausted(r.Context(), u) {
		writeErr(w, http.StatusTooManyRequests, "Daily compute time used up. It resets at midnight UTC.")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeErr(w, http.StatusUpgradeRequired, "This endpoint is a WebSocket.")
		return
	}
	rows, _ := strconv.ParseUint(r.URL.Query().Get("rows"), 10, 16)
	cols, _ := strconv.ParseUint(r.URL.Query().Get("cols"), 10, 16)

	cid, err := a.ensureSandbox(r.Context(), s, u)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	var secretEnv []string
	if env, _, err := a.Secrets.Snapshot(r.Context(), u.ID); err == nil {
		secretEnv = mergeEnv(nil, env) // read now: the shell sees what Settings holds at this moment
	}
	conn, err := websocket.Accept(w, r, nil) // same-origin only (the library checks Origin against Host)
	if err != nil {
		return // Accept has already replied
	}
	conn.SetReadLimit(1 << 20)
	// ctx ends the terminal; the socket itself is read and written with the request
	// context, because the WebSocket library drops the connection when the context it
	// is given is cancelled, which would skip the close handshake (and the reason).
	parent := r.Context()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	tty, err := a.Sandbox.OpenTerminal(ctx, s.ID, cid, uint(rows), uint(cols), secretEnv)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "could not start a shell: "+err.Error())
		return
	}
	defer tty.Close()
	cur := a.Terms.register(s.ID, cancel)
	defer a.Terms.remove(s.ID, cur)
	_, _ = a.Store.AppendAudit(context.Background(), fmt.Sprintf("user:%d", u.ID), "terminal.open", "session:"+s.ID, "")
	defer func() {
		_, _ = a.Store.AppendAudit(context.Background(), fmt.Sprintf("user:%d", u.ID), "terminal.close", "session:"+s.ID, "")
	}()

	// shell output -> browser
	go func() {
		defer cancel()
		buf := make([]byte, 16<<10)
		for {
			n, err := tty.Conn.Read(buf)
			if n > 0 {
				if werr := conn.Write(parent, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// keep the sandbox from being reaped while the terminal is open
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.Sandbox.Touch(s.ID)
			}
		}
	}()

	// browser -> shell
	last := time.Now()
	go func() { // idle watchdog
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(last) > terminalIdle {
					cur.setReason("Closed after 15 minutes without input.")
					cancel()
					return
				}
			}
		}
	}()
	go func() { // closing the context closes the socket so the read below returns
		<-ctx.Done()
		reason := cur.why()
		if reason == "" {
			reason = "Terminal closed."
		}
		_ = conn.Close(websocket.StatusNormalClosure, reason)
	}()
	for {
		typ, data, err := conn.Read(parent)
		if err != nil {
			return
		}
		last = time.Now()
		switch typ {
		case websocket.MessageBinary:
			if _, err := tty.Conn.Write(data); err != nil && !errors.Is(err, io.EOF) {
				return
			}
		case websocket.MessageText:
			var c termControl
			if json.Unmarshal(data, &c) == nil && c.Type == "resize" && c.Rows > 0 && c.Cols > 0 && c.Rows < 500 && c.Cols < 500 {
				_ = tty.Resize(ctx, c.Rows, c.Cols)
			}
		}
	}
}
