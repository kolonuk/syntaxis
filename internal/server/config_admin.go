// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kolonuk/syntaxis/internal/mail"
	"github.com/kolonuk/syntaxis/internal/secrets"
	"github.com/kolonuk/syntaxis/internal/settings"
)

// The configuration screen. Every option Syntaxis has is listed with the value in effect and where
// that value comes from: the Docker environment, a value saved in this app (which replaces it), or
// the default. A saved value can be removed to put the environment's back. Options that belong to
// the deployment are shown and not edited. See package settings for the rules.

// SettingsSecrets keeps write-only options (the mail password) as encrypted instance secrets.
type SettingsSecrets struct{ Svc *secrets.Service }

// Has only lists names, so showing the screen is not logged as reading the secret.
func (s SettingsSecrets) Has(ctx context.Context, name string) bool {
	names, _ := s.Svc.Names(ctx, secrets.ScopeInstance, 0)
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
func (s SettingsSecrets) Put(ctx context.Context, name, value string) error {
	return s.Svc.Put(ctx, secrets.ScopeInstance, 0, name, value)
}
func (s SettingsSecrets) Delete(ctx context.Context, name string) error {
	return s.Svc.Delete(ctx, secrets.ScopeInstance, 0, name)
}
func (s SettingsSecrets) Get(ctx context.Context, name string) (string, bool) {
	v, err := s.Svc.Get(ctx, secrets.ScopeInstance, 0, name)
	return v, err == nil
}

func (a *App) settingSecrets() settings.SecretStore { return SettingsSecrets{Svc: a.Secrets} }

// restartPending remembers which options were saved that only take effect after a restart.
func (a *App) markRestart(key string) {
	a.restartMu.Lock()
	defer a.restartMu.Unlock()
	if a.restartKeys == nil {
		a.restartKeys = map[string]time.Time{}
	}
	a.restartKeys[key] = a.Now()
}

func (a *App) restartPending() []string {
	a.restartMu.Lock()
	defer a.restartMu.Unlock()
	out := make([]string, 0, len(a.restartKeys))
	for k := range a.restartKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type settingsGroupJSON struct {
	ID    string          `json:"id"`
	Label string          `json:"label"`
	Help  string          `json:"help"`
	Items []settings.Item `json:"items"`
}

func (a *App) adminConfigGet(w http.ResponseWriter, r *http.Request) {
	items := settings.Resolve(r.Context(), a.Store, a.settingSecrets(), os.Getenv)
	groups := make([]settingsGroupJSON, 0, len(settings.Groups))
	for _, g := range settings.Groups {
		gj := settingsGroupJSON{ID: g.ID, Label: g.Label, Help: g.Help, Items: []settings.Item{}}
		for _, it := range items {
			if it.Group == g.ID {
				gj.Items = append(gj.Items, it)
			}
		}
		groups = append(groups, gj)
	}
	writeJSON(w, 200, map[string]any{"groups": groups, "restart_pending": a.restartPending()})
}

// adminConfigPut saves values. All are checked first; nothing is saved if any is refused.
func (a *App) adminConfigPut(w http.ResponseWriter, r *http.Request) {
	var b map[string]string
	if !decode(w, r, &b) {
		return
	}
	if len(b) == 0 {
		writeErr(w, 400, "nothing to save")
		return
	}
	for k, v := range b {
		if err := settings.Validate(k, v); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d, _ := settings.ByKey(k)
		detail := b[k]
		if d.Kind == settings.Secret {
			if b[k] == "" {
				continue // an empty box means "keep the current password"
			}
			if err := a.settingSecrets().Put(r.Context(), settings.SecretName(k), b[k]); err != nil {
				writeErr(w, 500, "could not save "+d.Label)
				return
			}
			detail = "(secret)"
		} else if err := a.Store.SetSetting(r.Context(), k, b[k]); err != nil {
			writeErr(w, 500, "could not save "+d.Label)
			return
		}
		if !d.Live {
			a.markRestart(k)
		}
		_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "setting.change", k, detail)
	}
	a.refreshHealth()
	writeJSON(w, 200, map[string]any{"ok": true, "restart_pending": a.restartPending()})
}

// adminConfigReset removes a saved value so the environment's (or the default) applies again.
func (a *App) adminConfigReset(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	d, ok := settings.ByKey(key)
	if !ok || d.Locked {
		writeErr(w, 404, "no such setting")
		return
	}
	var err error
	if d.Kind == settings.Secret {
		if a.settingSecrets().Has(r.Context(), settings.SecretName(key)) {
			err = a.settingSecrets().Delete(r.Context(), settings.SecretName(key))
		}
	} else {
		err = a.Store.DeleteSetting(r.Context(), key)
	}
	if err != nil {
		writeErr(w, 500, "could not reset")
		return
	}
	if !d.Live {
		a.markRestart(key)
	}
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "setting.reset", key, "")
	a.refreshHealth()
	writeJSON(w, 200, map[string]any{"ok": true, "restart_pending": a.restartPending()})
}

// MailFromSettings is the mailer that follows the mail settings in effect (see mail.Live).
func MailFromSettings(a *App) mail.Mailer {
	return mail.Live{
		Log: a.Log,
		Get: func(ctx context.Context) (mail.SMTP, bool) {
			host, port, user, pass, from := settings.SMTPFrom(ctx, a.Store, a.settingSecrets(), os.Getenv)
			if host == "" {
				return mail.SMTP{}, false
			}
			return mail.SMTP{Host: host, Port: port, User: user, Password: pass, From: from}, true
		},
	}
}
