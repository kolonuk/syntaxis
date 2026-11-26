// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type cfgItem struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Source   string `json:"source"`
	EnvValue string `json:"env_value"`
	HasValue bool   `json:"has_value"`
	Locked   bool   `json:"locked"`
	Live     bool   `json:"live"`
}

func getConfig(t *testing.T, adm *client) (map[string]cfgItem, []string, []byte) {
	t.Helper()
	code, m, raw := adm.do("GET", "/api/config", nil)
	if code != 200 {
		t.Fatalf("config: %d %s", code, raw)
	}
	var body struct {
		Groups []struct {
			ID    string    `json:"id"`
			Items []cfgItem `json:"items"`
		} `json:"groups"`
		Restart []string `json:"restart_pending"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	items := map[string]cfgItem{}
	for _, g := range body.Groups {
		for _, it := range g.Items {
			items[it.Key] = it
		}
	}
	_ = m
	return items, body.Restart, raw
}

// Each option shows the value in effect and where it comes from; a saved value replaces the
// Docker environment's, and removing it puts the environment's back.
func TestConfigScreenShowsWhereEachValueComesFrom(t *testing.T) {
	t.Setenv("SYNTAXIS_IDLE_MINUTES", "45")
	t.Setenv("SYNTAXIS_SMTP_HOST", "mail.docker.example")
	e := newEnv(t)
	adm := e.makeAdminSession()

	items, _, _ := getConfig(t, adm)
	if it := items["idle_minutes"]; it.Value != "45" || it.Source != "docker" {
		t.Fatalf("an environment value is flagged docker: %+v", it)
	}
	if it := items["cpu_minutes_daily"]; it.Value != "240" || it.Source != "default" {
		t.Fatalf("nothing set is the default: %+v", it)
	}
	if it := items["listen"]; !it.Locked {
		t.Fatalf("the listen address belongs to the deployment: %+v", it)
	}

	// save two: one applies at once, one after a restart
	code, m, raw := adm.do("PUT", "/api/config", map[string]string{"smtp_host": "mail.saved.example", "idle_minutes": "10"})
	if code != 200 {
		t.Fatalf("save: %d %s", code, raw)
	}
	restart, _ := m["restart_pending"].([]any)
	if len(restart) != 1 || restart[0] != "idle_minutes" {
		t.Fatalf("only an option that needs a restart is listed as waiting for one: %v", restart)
	}
	items, restartKeys, _ := getConfig(t, adm)
	if it := items["smtp_host"]; it.Value != "mail.saved.example" || it.Source != "database" || it.EnvValue != "mail.docker.example" || !it.Live {
		t.Fatalf("a saved value replaces the environment's and the environment's is still shown: %+v", it)
	}
	if it := items["idle_minutes"]; it.Value != "10" || it.Source != "database" || it.Live {
		t.Fatalf("%+v", it)
	}
	if len(restartKeys) != 1 {
		t.Fatalf("restart pending: %v", restartKeys)
	}

	// the saved values show in the older settings API too, which the Policies screen uses
	_, s, _ := adm.do("GET", "/api/settings", nil)
	if s["secret_scan_mode"] != "warn" {
		t.Fatalf("the Policies form must show the value in effect: %v", s["secret_scan_mode"])
	}

	// removing the saved value puts the environment's back
	if code, _, raw := adm.do("DELETE", "/api/config/smtp_host", nil); code != 200 {
		t.Fatalf("reset: %d %s", code, raw)
	}
	items, _, _ = getConfig(t, adm)
	if it := items["smtp_host"]; it.Value != "mail.docker.example" || it.Source != "docker" {
		t.Fatalf("%+v", it)
	}

	// every change is in the audit log
	es, _ := e.st.ListAudit(t.Context(), 0)
	var seen []string
	for _, x := range es {
		if strings.HasPrefix(x.Action, "setting.") {
			seen = append(seen, x.Action+" "+x.Target)
		}
	}
	for _, want := range []string{"setting.change smtp_host", "setting.change idle_minutes", "setting.reset smtp_host"} {
		if !strings.Contains(strings.Join(seen, "|"), want) {
			t.Errorf("audit log lacks %q: %v", want, seen)
		}
	}
}

// The Policies form used to show the built-in default for an option the environment had set.
func TestPoliciesFormShowsTheEnvironmentsValueNotTheDefault(t *testing.T) {
	t.Setenv("SYNTAXIS_SECRET_SCAN", "block")
	t.Setenv("SYNTAXIS_PASSWORD_ROTATION_DAYS", "90")
	e := newEnv(t)
	adm := e.makeAdminSession()
	_, s, _ := adm.do("GET", "/api/settings", nil)
	if s["secret_scan_mode"] != "block" || s["password_rotation_days"] != "90" {
		t.Fatalf("settings: %v", s)
	}
	adm.do("PUT", "/api/settings", map[string]string{"secret_scan_mode": "off"})
	_, s, _ = adm.do("GET", "/api/settings", nil)
	if s["secret_scan_mode"] != "off" {
		t.Fatalf("a saved value replaces the environment's: %v", s["secret_scan_mode"])
	}
	items, _, _ := getConfig(t, adm)
	if it := items["secret_scan_mode"]; it.Source != "database" || it.EnvValue != "block" {
		t.Fatalf("%+v", it)
	}
}

func TestConfigRefusesWhatIsNotAllowedAndSavesNothingOnAnyRefusal(t *testing.T) {
	e := newEnv(t)
	adm := e.makeAdminSession()
	for _, tc := range []struct {
		name string
		body map[string]string
	}{
		{"a deployment option", map[string]string{"listen": "0.0.0.0:80"}},
		{"the master key", map[string]string{"master_key": "x"}},
		{"an unknown option", map[string]string{"no_such_option": "x"}},
		{"a bad port", map[string]string{"smtp_port": "99999"}},
		{"a bad mode", map[string]string{"egress_mode": "everything"}},
		{"a header in the from address", map[string]string{"smtp_from": "a@example.com\nBcc: x@y.z"}},
		{"nothing at all", map[string]string{}},
		{"one good value with one bad", map[string]string{"idle_minutes": "5", "smtp_port": "0"}},
	} {
		if code, _, _ := adm.do("PUT", "/api/config", tc.body); code != 400 {
			t.Errorf("%s: %d", tc.name, code)
		}
	}
	items, restart, _ := getConfig(t, adm)
	if items["idle_minutes"].Source == "database" || len(restart) != 0 {
		t.Fatalf("a refused batch must save nothing: %+v %v", items["idle_minutes"], restart)
	}
	if code, _, _ := adm.do("DELETE", "/api/config/listen", nil); code != 404 {
		t.Fatalf("resetting a deployment option: %d", code)
	}
	if code, _, _ := adm.do("DELETE", "/api/config/nothing", nil); code != 404 {
		t.Fatalf("resetting an unknown option: %d", code)
	}
	usr := e.userSession("u@example.com")
	for _, p := range []string{"/api/config"} {
		if code, _, _ := usr.do("GET", p, nil); code == 200 {
			t.Errorf("a chat user read %s", p)
		}
	}
	if code, _, _ := e.client(e.admin.URL).do("GET", "/api/config", nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}

// The mail password is kept encrypted, never shown again, kept out of the credentials a server may
// be given, and is not in the audit log.
func TestMailPasswordIsWriteOnly(t *testing.T) {
	e := newEnv(t)
	adm := e.makeAdminSession()
	const pw = "correct-horse-battery"
	if code, _, raw := adm.do("PUT", "/api/config", map[string]string{"smtp_password": pw, "smtp_host": "mail.example.com"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	items, _, raw := getConfig(t, adm)
	if it := items["smtp_password"]; !it.HasValue || it.Source != "database" || it.Value != "" {
		t.Fatalf("%+v", it)
	}
	if strings.Contains(string(raw), pw) {
		t.Fatal("the password came back in the configuration")
	}
	// an empty box keeps the saved one
	adm.do("PUT", "/api/config", map[string]string{"smtp_password": "", "smtp_port": "25"})
	if it, _, _ := getConfig(t, adm); !it["smtp_password"].HasValue {
		t.Fatal("an empty password box erased the saved password")
	}
	// it is stored encrypted, not as plain text in any table
	if ct, err := e.st.GetSecret(t.Context(), "instance", 0, "SYNTAXIS_SMTP_PASSWORD"); err != nil || strings.Contains(string(ct.Ciphertext), pw) || len(ct.Ciphertext) == 0 {
		t.Fatalf("not stored as an encrypted secret: %v", err)
	}
	// the admin has no secrets screen or route any more: servers take only each user's own credentials
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		if code, _, body := adm.do(m, "/api/secrets/SYNTAXIS_SMTP_PASSWORD", map[string]string{"value": "x"}); code != 404 || strings.Contains(string(body), "SYNTAXIS_SMTP_PASSWORD") {
			t.Fatalf("%s on the admin secrets route: %d %s", m, code, body)
		}
	}
	es, _ := e.st.ListAudit(t.Context(), 0)
	for _, x := range es {
		if strings.Contains(x.Detail, pw) || strings.Contains(x.Target, pw) {
			t.Fatalf("the password is in the audit log: %+v", x)
		}
	}
	// removing the saved one
	if code, _, _ := adm.do("DELETE", "/api/config/smtp_password", nil); code != 200 {
		t.Fatal("reset")
	}
	if it, _, _ := getConfig(t, adm); it["smtp_password"].HasValue {
		t.Fatal("the password is still set after a reset")
	}
}

// A mail server saved in the app is the one used for the next email, without a restart; with none,
// the email is only logged. Uses the real mailer, so a connection to a closed port really fails.
func TestSavedMailServerIsUsedForTheNextEmail(t *testing.T) {
	e := newEnv(t)
	e.app.Mail = MailFromSettings(e.app)
	adm := e.makeAdminSession()

	code, m, raw := adm.do("POST", "/api/mail/test", nil)
	if code != 200 || m["sent"] != false {
		t.Fatalf("with no mail server the test email is only logged: %d %s", code, raw)
	}
	adm.do("PUT", "/api/config", map[string]string{"smtp_host": "127.0.0.1", "smtp_port": "1"}) // nothing listens on port 1
	code, m, _ = adm.do("POST", "/api/mail/test", nil)
	if code != 502 || !strings.Contains(m["error"].(string), "sending failed") {
		t.Fatalf("a saved mail server must be used at once (and fail here, nothing listens): %d %v", code, m)
	}
	adm.do("DELETE", "/api/config/smtp_host", nil)
	if code, _, _ := adm.do("POST", "/api/mail/test", nil); code != 200 {
		t.Fatalf("after the reset the email is only logged again: %d", code)
	}
}

// Health is refreshed when something it depends on changes, not up to five minutes later.
func TestHealthIsRefreshedWhenConfigurationOrAdminsChange(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	n := 0
	e.app.RefreshHealth = func() { mu.Lock(); n++; mu.Unlock() }
	adm := e.makeAdminSession()
	count := func() int {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return n
	}
	before := count()
	adm.do("PUT", "/api/config", map[string]string{"smtp_host": "mail.example.com"})
	adm.do("DELETE", "/api/config/smtp_host", nil)
	adm.do("POST", "/api/admins", map[string]string{"email": "new@example.com", "name": "New"})
	if got := count() - before; got < 3 {
		t.Fatalf("health was refreshed %d times after three changes", got)
	}
}
