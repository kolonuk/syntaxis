// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/store"
)

// A hosted service as far as Syntaxis can tell: an OpenAI-compatible API that wants a bearer key.
type fakeHosted struct {
	*httptest.Server
	key  string
	mu   sync.Mutex
	hits int
}

func newFakeHosted(t *testing.T) *fakeHosted {
	t.Helper()
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	f := &fakeHosted{key: "k-" + hex.EncodeToString(b)}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits++
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+f.key {
			http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"beta"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// userProvidersEnv is an instance where users may bring providers, with the service on this machine
// (the test switch opens loopback and plain http for it; production opens neither).
func userProvidersEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.app.userProviderTest = true
	if err := e.st.SetSetting(context.Background(), settingUserProviders, "true"); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) userID(email string) int64 {
	e.t.Helper()
	a, err := e.st.GetAccountByEmail(context.Background(), store.KindUser, email)
	if err != nil {
		e.t.Fatal(err)
	}
	return a.ID
}

// firstSessionID is the id of the user's only (or first) session.
func (e *env) firstSessionID(c *client) string {
	e.t.Helper()
	_, list, raw := c.do("GET", "/api/sessions", nil)
	_ = list
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || len(out) == 0 {
		e.t.Fatalf("sessions: %s", raw)
	}
	return out[0]["id"].(string)
}

func addProvider(t *testing.T, c *client, name, base, key string) (int, map[string]any, []byte) {
	t.Helper()
	return c.do("POST", "/api/my/providers", map[string]any{"name": name, "base_url": base, "api_key": key})
}

// Providers of your own are off until an admin turns them on, and nothing can be added meanwhile.
func TestUserProvidersAreOffUntilAnAdminTurnsThemOn(t *testing.T) {
	e := newEnv(t)
	e.app.userProviderTest = true
	f := newFakeHosted(t)
	ann := e.userSession("ann-prov@example.com")
	code, res, _ := ann.do("GET", "/api/my/providers", nil)
	if code != 200 || res["allowed"] != false {
		t.Fatalf("default: %d %v", code, res)
	}
	if code, _, _ := addProvider(t, ann, "hosted", f.URL, f.key); code != 403 {
		t.Fatalf("added while off: %d", code)
	}
	f.mu.Lock()
	hits := f.hits
	f.mu.Unlock()
	if hits != 0 {
		t.Fatal("the service was contacted while the feature was off")
	}
	if got, _ := e.st.ListUserProviders(context.Background(), 0); len(got) != 0 {
		t.Fatalf("stored: %v", got)
	}
}

// Only a public service is accepted: names that are private, plain http, loopback, link-local and
// cloud metadata addresses are all refused before anything is stored.
func TestUserProviderAddressesMustBePublic(t *testing.T) {
	e := newEnv(t) // production behaviour: no test switch
	if err := e.st.SetSetting(context.Background(), settingUserProviders, "true"); err != nil {
		t.Fatal(err)
	}
	ann := e.userSession("ann-addr@example.com")
	for _, base := range []string{
		"http://api.example.com",          // not https
		"https://127.0.0.1",               // loopback
		"https://127.0.0.1:8081/v1",       // the admin app's address
		"https://localhost",               // loopback by name
		"https://[::1]",                   // loopback, IPv6
		"https://10.1.2.3",                // private
		"https://192.168.1.10/v1",         // private
		"https://172.16.0.5",              // private
		"https://169.254.169.254",         // cloud metadata
		"https://0.0.0.0",                 // this host
		"https://intranet",                // a name with no dot: a LAN host
		"https://printer.local",           // a LAN name
		"https://user:pw@api.example.com", // credentials in the address
		"https://api.example.com/v1?x=1",  // a query
		"ftp://api.example.com",
	} {
		if code, _, raw := addProvider(t, ann, "x", base, "key"); code != 400 {
			t.Errorf("%s: %d %s", base, code, raw)
		}
	}
	if got, _ := e.st.ListUserProviders(context.Background(), 0); len(got) != 0 {
		t.Fatalf("something was stored: %v", got)
	}
}

// The client checks every address it connects to, so a redirect to a private or metadata address
// is refused even when the first address was fine.
func TestUserProviderClientRefusesRedirectsToPrivateAddresses(t *testing.T) {
	e := newEnv(t)
	e.app.userProviderTest = true // the first hop is on this machine; the redirect targets are not
	for _, target := range []string{"http://169.254.169.254/latest/meta-data/", "http://10.0.0.7/", "http://192.168.0.1/"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		resp, err := e.app.userProviderClient().Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		cancel()
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("redirect to %s: %v", target, err)
		}
	}
	// without the test switch not even this machine is reachable
	e.app.userProviderTest = false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	if resp, err := e.app.userProviderClient().Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a loopback service was reachable")
	}
}

func TestUserProviderLifecycleAndIsolation(t *testing.T) {
	e := userProvidersEnv(t)
	f := newFakeHosted(t)
	ann := e.userSession("ann-life@example.com")
	bob := e.userSession("bob-life@example.com")
	adm := e.makeAdminSession()

	// a wrong key is refused and nothing is stored
	if code, _, raw := addProvider(t, ann, "hosted", f.URL+"/v1", "not-the-key"); code != 400 || !strings.Contains(string(raw), "did not accept") || strings.Contains(string(raw), "not-the-key") {
		t.Fatalf("wrong key: %d %s", code, raw)
	}
	// the right one works; the address may end in /v1 as the service's documentation says
	code, res, raw := addProvider(t, ann, "hosted", f.URL+"/v1", f.key)
	if code != 200 {
		t.Fatalf("add: %d %s", code, raw)
	}
	if strings.Contains(string(raw), f.key) {
		t.Fatal("the key is in the response")
	}
	id := int64(res["id"].(float64))
	if ms, _ := res["models"].([]any); len(ms) != 2 {
		t.Fatalf("models: %v", res["models"])
	}
	if code, _, _ := addProvider(t, ann, "hosted", f.URL, f.key); code != 409 {
		t.Fatalf("duplicate name: %d", code)
	}

	// the key is sealed, not stored in the clear, and is in no response or audit entry
	p, _ := e.st.GetProvider(context.Background(), id)
	if strings.Contains(string(p.APIKeyEnc), f.key) || len(p.APIKeyEnc) == 0 || p.OwnerID == 0 {
		t.Fatalf("stored provider: %+v", p)
	}
	for _, c := range []*client{ann, adm} {
		for _, path := range []string{"/api/my/providers", "/api/models", "/api/user-providers", "/api/audit"} {
			base := c
			if _, body := func() (int, []byte) { code, _, b := base.do("GET", path, nil); return code, b }(); strings.Contains(string(body), f.key) {
				t.Fatalf("the key is in %s", path)
			}
		}
	}

	// nobody else can see, change, refresh, enable a model of, or delete it
	_, mine, _ := bob.do("GET", "/api/my/providers", nil)
	if l, _ := mine["providers"].([]any); len(l) != 0 {
		t.Fatalf("bob sees %v", l)
	}
	idPath := "/api/my/providers/" + itoa(id)
	for _, c := range []struct{ method, path string }{
		{"PUT", idPath}, {"DELETE", idPath}, {"POST", idPath + "/refresh"}, {"PUT", idPath + "/models/1"},
	} {
		if code, _, _ := bob.do(c.method, c.path, map[string]any{"name": "mine", "enabled": true}); code != 404 {
			t.Errorf("bob %s %s: %d", c.method, c.path, code)
		}
	}
	if got, _ := e.st.GetProvider(context.Background(), id); got == nil {
		t.Fatal("bob deleted ann's provider")
	}

	// models start off; once ann turns one on it is hers alone, marked as leaving the machine
	_, mine, _ = ann.do("GET", "/api/my/providers", nil)
	ms := mine["providers"].([]any)[0].(map[string]any)["models"].([]any)
	var alpha int64
	for _, m := range ms {
		mm := m.(map[string]any)
		if mm["enabled"] != false {
			t.Fatalf("a model started on: %v", mm)
		}
		if mm["name"] == "alpha" {
			alpha = int64(mm["id"].(float64))
		}
	}
	if code, _, raw := ann.do("PUT", idPath+"/models/"+itoa(alpha), map[string]any{"enabled": true, "context": 65536}); code != 200 {
		t.Fatalf("enable: %d %s", code, raw)
	}
	if code, _, _ := ann.do("PUT", idPath+"/models/"+itoa(alpha), map[string]any{"context": 5}); code != 400 {
		t.Fatalf("a tiny window was accepted: %d", code)
	}
	_, list, _ := ann.do("GET", "/api/models", nil)
	_ = list
	code, _, raw = ann.do("GET", "/api/models", nil)
	if !strings.Contains(string(raw), `"@hosted/alpha"`) || !strings.Contains(string(raw), `"external":true`) || strings.Contains(string(raw), "beta") {
		t.Fatalf("ann's models: %d %s", code, raw)
	}
	if _, _, raw = bob.do("GET", "/api/models", nil); strings.Contains(string(raw), "hosted") {
		t.Fatalf("bob's models: %s", raw)
	}

	// she can start a session on it; he cannot, and the audit log names the provider used
	code, sess, raw := ann.do("POST", "/api/sessions", map[string]string{"model": "@hosted/alpha"})
	if code != 201 {
		t.Fatalf("ann's session: %d %s", code, raw)
	}
	if code, _, _ := bob.do("POST", "/api/sessions", map[string]string{"model": "@hosted/alpha"}); code != 403 {
		t.Fatalf("bob used ann's provider: %d", code)
	}
	if code, _, _ := ann.do("POST", "/api/sessions", map[string]string{"model": "@hosted/beta"}); code != 403 {
		t.Fatalf("a model that is off was used: %d", code)
	}
	audit := ""
	if es, _ := e.st.ListAudit(context.Background(), 0); len(es) > 0 {
		for _, x := range es {
			audit += x.Action + " " + x.Target + " " + x.Detail + "\n"
		}
	}
	for _, want := range []string{"userprovider.add", "userprovider.use session:" + sess["id"].(string)} {
		if !strings.Contains(audit, want) {
			t.Errorf("no audit entry %q in:\n%s", want, audit)
		}
	}

	// the admin sees names and addresses with the owner, never the key, and can remove it
	code, _, raw = adm.do("GET", "/api/user-providers", nil)
	if code != 200 || !strings.Contains(string(raw), "ann-life@example.com") || !strings.Contains(string(raw), f.URL) {
		t.Fatalf("admin's view: %d %s", code, raw)
	}
	if code, _, _ := e.client(e.admin.URL).do("GET", "/api/user-providers", nil); code != 401 && code != 403 {
		t.Fatalf("the admin list was readable anonymously: %d", code)
	}
	if code, _, _ := ann.do("GET", "/api/user-providers", nil); code != 404 && code != 401 && code != 403 {
		t.Fatalf("a user read the admin list: %d", code)
	}

	// an admin removal stops a running session on it and the provider is gone
	run, _ := e.app.Runs.Start(sess["id"].(string), func(ctx context.Context, _ *Run) { <-ctx.Done() })
	if code, _, raw := adm.do("DELETE", "/api/user-providers/"+itoa(id), nil); code != 200 {
		t.Fatalf("remove: %d %s", code, raw)
	}
	stopped := make(chan struct{})
	go func() { run.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the running session was not stopped")
	}
	if got, err := e.st.GetProvider(context.Background(), id); err == nil {
		t.Fatalf("still there: %+v", got)
	}
	if code, _, _ := ann.do("POST", "/api/sessions", map[string]string{"model": "@hosted/alpha"}); code != 403 {
		t.Fatalf("a removed provider was used: %d", code)
	}
}

// With the instance switch off nothing can be added or used by any route, whatever was added before;
// an admin can still turn it on for one user.
func TestUserProvidersSwitches(t *testing.T) {
	e := userProvidersEnv(t)
	f := newFakeHosted(t)
	ann := e.userSession("ann-sw@example.com")
	adm := e.makeAdminSession()
	annID := e.userID("ann-sw@example.com")

	code, res, raw := addProvider(t, ann, "hosted", f.URL, f.key)
	if code != 200 {
		t.Fatalf("add: %d %s", code, raw)
	}
	id := int64(res["id"].(float64))
	ms := res["models"].([]any)
	m0 := int64(ms[0].(map[string]any)["id"].(float64))
	if code, _, _ := ann.do("PUT", "/api/my/providers/"+itoa(id)+"/models/"+itoa(m0), map[string]any{"enabled": true}); code != 200 {
		t.Fatalf("enable: %d", code)
	}
	name := ms[0].(map[string]any)["name"].(string)
	model := "@hosted/" + name
	if code, _, _ := ann.do("POST", "/api/sessions", map[string]string{"model": model}); code != 201 {
		t.Fatalf("session: %d", code)
	}

	// the instance switch off, through the admin app: a running session stops, and every route closes
	sess := e.firstSessionID(ann)
	run, _ := e.app.Runs.Start(sess, func(ctx context.Context, _ *Run) { <-ctx.Done() })
	if code, _, raw := adm.do("PUT", "/api/settings", map[string]string{settingUserProviders: "false"}); code != 200 {
		t.Fatalf("switch off: %d %s", code, raw)
	}
	done := make(chan struct{})
	go func() { run.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("switching the feature off did not stop the session")
	}
	if _, res, _ := ann.do("GET", "/api/my/providers", nil); res["allowed"] != false || len(res["providers"].([]any)) != 0 {
		t.Fatalf("listed while off: %v", res)
	}
	if _, _, raw := ann.do("GET", "/api/models", nil); strings.Contains(string(raw), "hosted") {
		t.Fatalf("models while off: %s", raw)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/my/providers"}, {"PUT", "/api/my/providers/" + itoa(id)}, {"POST", "/api/my/providers/" + itoa(id) + "/refresh"},
		{"PUT", "/api/my/providers/" + itoa(id) + "/models/" + itoa(m0)},
	} {
		if code, _, _ := ann.do(c.method, c.path, map[string]any{"name": "x", "base_url": f.URL, "api_key": f.key, "enabled": true}); code != 403 {
			t.Errorf("%s %s while off: %d", c.method, c.path, code)
		}
	}
	if code, _, _ := ann.do("POST", "/api/sessions", map[string]string{"model": model}); code != 403 {
		t.Fatalf("session while off: %d", code)
	}

	// one user can be given it while the instance is off, and taken away again
	if code, _, raw := adm.do("PUT", "/api/users/"+itoa(annID)+"/own-providers", map[string]string{"value": "on"}); code != 200 {
		t.Fatalf("on for ann: %d %s", code, raw)
	}
	if code, _, _ := ann.do("POST", "/api/sessions", map[string]string{"model": model}); code != 201 {
		t.Fatalf("session with her switch on: %d", code)
	}
	if code, _, _ := adm.do("PUT", "/api/users/"+itoa(annID)+"/own-providers", map[string]string{"value": "off"}); code != 200 {
		t.Fatal("off for ann")
	}
	if code, _, _ := ann.do("POST", "/api/sessions", map[string]string{"model": model}); code != 403 {
		t.Fatalf("session with her switch off: %d", code)
	}
	if code, _, _ := adm.do("PUT", "/api/users/"+itoa(annID)+"/own-providers", map[string]string{"value": "maybe"}); code != 400 {
		t.Fatalf("a bad value: %d", code)
	}
	// a user cannot flip their own switch
	if code, _, _ := ann.do("PUT", "/api/users/"+itoa(annID)+"/own-providers", map[string]string{"value": "on"}); code == 200 {
		t.Fatal("a user changed their own switch")
	}
	// the admin's settings view shows it
	if _, v, _ := adm.do("GET", "/api/users/"+itoa(annID)+"/settings", nil); v["own_providers"] != "off" || len(v["providers"].([]any)) != 1 {
		t.Fatalf("the admin's view of the user: %v", v)
	}
}

// A model on the admin's allow-list for a user is the only own-provider model that user may use.
func TestAdminAllowListAlsoCoversOwnProviderModels(t *testing.T) {
	e := userProvidersEnv(t)
	f := newFakeHosted(t)
	ann := e.userSession("ann-allow@example.com")
	adm := e.makeAdminSession()
	code, res, raw := addProvider(t, ann, "hosted", f.URL, f.key)
	if code != 200 {
		t.Fatalf("add: %d %s", code, raw)
	}
	id := int64(res["id"].(float64))
	for _, m := range res["models"].([]any) {
		mid := int64(m.(map[string]any)["id"].(float64))
		ann.do("PUT", "/api/my/providers/"+itoa(id)+"/models/"+itoa(mid), map[string]any{"enabled": true})
	}
	uid := e.userID("ann-allow@example.com")
	if code, _, raw := adm.do("PATCH", "/api/users/"+itoa(uid), map[string]any{"allowed_models": []string{"@hosted/beta"}}); code != 200 {
		t.Fatalf("allow-list: %d %s", code, raw)
	}
	_, _, raw = ann.do("GET", "/api/models", nil)
	if strings.Contains(string(raw), "@hosted/alpha") || !strings.Contains(string(raw), "@hosted/beta") {
		t.Fatalf("models: %s", raw)
	}
	if code, _, _ := ann.do("POST", "/api/sessions", map[string]string{"model": "@hosted/alpha"}); code != 403 {
		t.Fatalf("a model outside the allow-list: %d", code)
	}
	if code, _, _ := ann.do("POST", "/api/sessions", map[string]string{"model": "@hosted/beta"}); code != 201 {
		t.Fatalf("a model on the allow-list: %d", code)
	}
}

// A user's provider is not in any admin list, so no admin screen or health check treats it as local.
func TestUserProvidersStayOutOfTheAdminProviderLists(t *testing.T) {
	e := userProvidersEnv(t)
	f := newFakeHosted(t)
	ann := e.userSession("ann-lists@example.com")
	adm := e.makeAdminSession()
	if code, _, raw := addProvider(t, ann, "hosted", f.URL, f.key); code != 200 {
		t.Fatalf("add: %d %s", code, raw)
	}
	_, _, raw := adm.do("GET", "/api/providers", nil)
	if strings.Contains(string(raw), "hosted") || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("admin providers: %s", raw)
	}
	_, _, raw = adm.do("GET", "/api/models", nil)
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("admin models: %s", raw)
	}
	if ps, _ := e.st.ListProviders(context.Background()); len(ps) != 0 {
		t.Fatalf("ListProviders: %v", ps)
	}
}
