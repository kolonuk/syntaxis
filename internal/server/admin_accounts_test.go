// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/audit"
	"github.com/kolonuk/syntaxis/internal/store"
)

// Admin accounts can be listed, added, reset, disabled and removed from the admin app, and every
// rule that stops an admin locking everyone out holds.
func TestAdminAccountsAreMaintainedFromTheAdminApp(t *testing.T) {
	e := newEnv(t)
	adm := e.makeAdminSession()
	ctx := context.Background()

	code, _, raw := adm.do("GET", "/api/admins", nil)
	if code != 200 || !strings.Contains(string(raw), `"you":true`) || !strings.Contains(string(raw), "boss@example.com") {
		t.Fatalf("list: %d %s", code, raw)
	}

	// invite a second admin: a single-use link comes back, the account is pending until it is used
	code, m, raw := adm.do("POST", "/api/admins", map[string]string{"email": "second@example.com", "name": "Second Admin"})
	if code != 201 {
		t.Fatalf("invite: %d %s", code, raw)
	}
	path, _ := m["setup_path"].(string)
	if !strings.HasPrefix(path, "/#setup=") {
		t.Fatalf("the link must be a URL fragment so it never reaches a log: %q", path)
	}
	second := int64(m["id"].(float64))
	if code, _, _ := adm.do("POST", "/api/admins", map[string]string{"email": "second@example.com", "name": "x"}); code != 201 {
		t.Fatalf("a second link for a still-pending admin must be allowed, got %d", code)
	}
	if code, _, _ := adm.do("POST", "/api/admins", map[string]string{"email": "not-an-email", "name": "x"}); code != 400 {
		t.Fatalf("a bad email was accepted: %d", code)
	}
	if code, _, _ := adm.do("POST", "/api/admins", map[string]string{"email": "nameless@example.com"}); code != 400 {
		t.Fatalf("an admin without a name was accepted: %d", code)
	}
	acct, _ := e.st.GetAccount(ctx, store.KindAdmin, second)
	if acct.PasswordHash != "" {
		t.Fatal("a pending admin must not have a password")
	}

	// the link works once and the admin then has a password but still no 2FA
	pub := e.client(e.admin.URL)
	tok := strings.TrimPrefix(path, "/#setup=")
	if code, _, raw := pub.do("POST", "/api/setup", map[string]string{"token": tok, "password": pw}); code != 200 {
		t.Fatalf("setup: %d %s", code, raw)
	}

	// resetting someone else clears password and 2FA and hands back a new link; resetting yourself is refused
	e.st.UpdateAccount(ctx, store.Account{ID: second, Kind: store.KindAdmin, Email: "second@example.com", Name: "Second Admin", PasswordHash: "x", TOTPEnabled: true, CreatedAt: acct.CreatedAt})
	code, m, raw = adm.do("POST", fmt.Sprintf("/api/admins/%d/reset", second), nil)
	if code != 200 || !strings.HasPrefix(m["setup_path"].(string), "/#setup=") {
		t.Fatalf("reset: %d %s", code, raw)
	}
	acct, _ = e.st.GetAccount(ctx, store.KindAdmin, second)
	if acct.PasswordHash != "" || acct.TOTPEnabled {
		t.Fatalf("reset must clear the password and 2FA: %+v", acct)
	}
	me, _ := e.st.GetAccountByEmail(ctx, store.KindAdmin, "boss@example.com")
	if code, _, _ := adm.do("POST", fmt.Sprintf("/api/admins/%d/reset", me.ID), nil); code != 403 {
		t.Fatalf("resetting your own account: %d", code)
	}

	// disable and enable; you cannot disable yourself
	if code, _, _ := adm.do("PATCH", fmt.Sprintf("/api/admins/%d", second), map[string]any{"disabled": true}); code != 200 {
		t.Fatalf("disable: %d", code)
	}
	if code, _, _ := adm.do("PATCH", fmt.Sprintf("/api/admins/%d", me.ID), map[string]any{"disabled": true}); code != 403 {
		t.Fatalf("disabling yourself: %d", code)
	}
	if code, _, _ := adm.do("PATCH", fmt.Sprintf("/api/admins/%d", second), map[string]any{}); code != 400 {
		t.Fatalf("an empty change: %d", code)
	}
	if code, _, _ := adm.do("PATCH", fmt.Sprintf("/api/admins/%d", second), map[string]any{"disabled": false}); code != 200 {
		t.Fatalf("enable: %d", code)
	}

	// remove: not yourself; the other admin can go because you remain
	if code, _, _ := adm.do("DELETE", fmt.Sprintf("/api/admins/%d", me.ID), nil); code != 403 {
		t.Fatalf("removing yourself: %d", code)
	}
	if code, _, _ := adm.do("DELETE", fmt.Sprintf("/api/admins/%d", second), nil); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	if _, err := e.st.GetAccount(ctx, store.KindAdmin, second); err == nil {
		t.Fatal("the removed admin is still there")
	}
	if code, _, _ := adm.do("DELETE", "/api/admins/9999", nil); code != 404 {
		t.Fatalf("removing a missing admin: %d", code)
	}

	// everything was audited, with who did it
	es, _ := e.st.ListAudit(ctx, 0)
	var actions []string
	for _, x := range es {
		actions = append(actions, x.Action)
	}
	joined := strings.Join(actions, " ")
	for _, want := range []string{"admin.setup-link", "admin.reset", "admin.disable", "admin.enable", "admin.remove"} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit log has no %s: %s", want, joined)
		}
	}
}

// The audit tab can show what "verified" means: how many entries, and the head hash that pins the
// whole log. The export verifies on its own and a changed line is caught.
func TestAuditShowsItsHeadAndExportsAVerifiableLog(t *testing.T) {
	e := newEnv(t)
	adm := e.makeAdminSession()
	_, _, _ = adm.do("POST", "/api/admins", map[string]string{"email": "x@example.com", "name": "X"})

	code, m, raw := adm.do("GET", "/api/audit", nil)
	if code != 200 || m["chain_ok"] != true {
		t.Fatalf("audit: %d %s", code, raw)
	}
	head, _ := m["head"].(map[string]any)
	total := int(m["total"].(float64))
	if total < 2 || int(head["count"].(float64)) != total || len(head["hash"].(string)) != 64 || int(head["seq"].(float64)) != total {
		t.Fatalf("head %v total %d", head, total)
	}
	if m["genesis"] != strings.Repeat("0", 64) {
		t.Fatalf("genesis %v", m["genesis"])
	}

	code, _, body := adm.do("GET", "/api/audit/export", nil)
	if code != 200 {
		t.Fatalf("export: %d", code)
	}
	entries, err := audit.ReadLines(strings.NewReader(string(body)))
	if err != nil || len(entries) != total {
		t.Fatalf("%d entries, %v", len(entries), err)
	}
	if audit.Verify(entries) != -1 || audit.HeadOf(entries).Hash != head["hash"] {
		t.Fatal("the export must verify on its own and end at the head the tab shows")
	}
	tampered := strings.Replace(string(body), "admin.setup-link", "admin.nothing", 1)
	back, _ := audit.ReadLines(strings.NewReader(tampered))
	if audit.Verify(back) == -1 {
		t.Fatal("an edited export verified")
	}
	// only a signed-in admin may download it
	if code, _, _ := e.client(e.admin.URL).do("GET", "/api/audit/export", nil); code != 401 {
		t.Fatalf("anonymous export: %d", code)
	}
}
