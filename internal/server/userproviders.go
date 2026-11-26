// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/safehttp"
	"github.com/kolonuk/syntaxis/internal/store"
)

// A user can bring a hosted model service of their own (an OpenAI-compatible API) with their own
// key. It is a provider row owned by that user: only they can see, change or use it, the admin can
// switch the feature off for everyone or for one user, and the admin sees and removes what users
// added. The key is sealed with the server's vault like a provider key and never leaves the server.
// The address must be a public one: the server resolves the name itself and refuses private,
// loopback and link-local addresses at connection time, after redirects included (package safehttp).
// Local and LAN model servers are the admin's to add.
const (
	// settingUserProviders is the instance switch; settingUserProvidersUser is a user's own
	// override ("on" or "off"), which wins over it.
	settingUserProviders     = "user_providers"
	settingUserProvidersUser = "user_providers_user:"
	maxUserProviders         = 5 // per user
	userProviderConcurrency  = 2 // requests at once to one user's provider, unless they set less
)

var userProviderNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

// userProviderName is the name stored for a user's provider: unique across users and apart from the
// admin's, which never start with "@".
func userProviderName(userID int64, display string) string {
	return fmt.Sprintf("@%d:%s", userID, display)
}

// userProvidersAllowed says whether this user may have providers of their own: their own override
// if the admin set one, else the instance switch (off until an admin turns it on).
func (a *App) userProvidersAllowed(ctx context.Context, userID int64) bool {
	switch a.setting(ctx, settingUserProvidersUser+itoa(userID), "") {
	case "on":
		return true
	case "off":
		return false
	}
	return a.settingBool(ctx, settingUserProviders, false)
}

// checkUserProviderURL accepts the address of a hosted service and returns it without a trailing
// slash or "/v1" (the adapter adds that). Only https to a public name or address is accepted.
func (a *App) checkUserProviderURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if err := safehttp.CheckBaseURL(raw); err != nil {
		return "", errors.New("use the address from the service's documentation, like https://api.example.com/v1")
	}
	u, _ := url.Parse(raw)
	if u.Scheme != "https" && !a.userProviderTest {
		return "", errors.New("the address must start with https://")
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if why := safehttp.Check(ip, a.userProviderAllow()); why != "" {
			return "", fmt.Errorf("that address is not allowed (%s); only public services can be added here", why)
		}
	} else if h := strings.ToLower(host); h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") || !strings.Contains(h, ".") && !a.userProviderTest {
		return "", errors.New("only public services can be added here")
	}
	out := strings.TrimRight(raw, "/")
	out = strings.TrimSuffix(out, "/v1")
	return strings.TrimRight(out, "/"), nil
}

// userProviderAllow is empty in production: no private range is ever opened for a user's provider.
// Tests that run a service on this machine set userProviderTest, which opens loopback only.
func (a *App) userProviderAllow() []netip.Prefix {
	if a.userProviderTest {
		return []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	}
	return nil
}

// userProviderClient is the HTTP client for a user's provider: it checks every address it connects
// to and has no overall timeout (a reply streams), but waits at most two minutes for the first byte.
func (a *App) userProviderClient() *http.Client {
	c := safehttp.NewClient(0, a.userProviderAllow()...)
	if tr, ok := c.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = 2 * time.Minute
	}
	return c
}

// userProviderFor builds the adapter for a user's provider (decrypting its key).
func (a *App) userProviderFor(p *store.Provider) (provider.Provider, error) {
	key := ""
	if len(p.APIKeyEnc) > 0 {
		pt, err := a.Secrets.Vault.Open(p.APIKeyEnc, "provider:"+p.Name)
		if err != nil {
			return nil, err
		}
		key = string(pt)
	}
	return provider.New(provider.Config{Kind: provider.KindOpenAI, BaseURL: p.BaseURL, APIKey: key, Client: a.userProviderClient(), Retries: 2})
}

// userModelID is how a user's models are named in lists and requests: "@name/model". The admin's
// providers cannot be named "@…", so the two never collide.
func userModelID(display, model string) string { return "@" + display + "/" + model }

// stopUserProviderRuns stops running sessions that use a user's provider: of one provider, or of
// everyone's that match keep == false for their owner.
func (a *App) stopUserProviderRuns(ctx context.Context, match func(p store.Provider) bool) {
	provs, _ := a.Store.ListUserProviders(ctx, 0)
	byID := map[int64]store.Provider{}
	for _, p := range provs {
		if match(p) {
			byID[p.ID] = p
		}
	}
	if len(byID) == 0 {
		return
	}
	sessions, _ := a.Store.ListAllChatSessions(ctx)
	for _, s := range sessions {
		if _, ok := byID[s.ProviderID]; !ok {
			continue
		}
		if run := a.Runs.Get(s.ID); run != nil {
			run.Stop()
		}
	}
}

// ---- the user's own screens ----

type myProviderJSON struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	BaseURL     string        `json:"base_url"`
	Concurrency int           `json:"concurrency"`
	HasKey      bool          `json:"has_key"`
	Models      []myModelJSON `json:"models"`
}

type myModelJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Context int    `json:"context"`
}

func (a *App) myProvider(w http.ResponseWriter, r *http.Request) (*store.Provider, bool) {
	u := a.user(r)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}
	p, err := a.Store.GetProvider(r.Context(), pathID(r))
	// somebody else's provider is "not found", the same as one that does not exist
	if err != nil || p.OwnerID != u.ID {
		writeErr(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return p, true
}

func (a *App) providerJSON(ctx context.Context, p store.Provider) myProviderJSON {
	out := myProviderJSON{ID: p.ID, Name: p.DisplayName, BaseURL: p.BaseURL, Concurrency: p.Concurrency, HasKey: len(p.APIKeyEnc) > 0, Models: []myModelJSON{}}
	ms, _ := a.Store.ListModels(ctx, p.ID)
	for _, m := range ms {
		out.Models = append(out.Models, myModelJSON{ID: m.ID, Name: m.Name, Enabled: m.Enabled, Context: m.ContextOverride})
	}
	return out
}

func (a *App) handleMyProviders(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	list := []myProviderJSON{}
	if a.userProvidersAllowed(r.Context(), u.ID) {
		ps, _ := a.Store.ListUserProviders(r.Context(), u.ID)
		for _, p := range ps {
			list = append(list, a.providerJSON(r.Context(), p))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"allowed": a.userProvidersAllowed(r.Context(), u.ID), "max": maxUserProviders, "providers": list})
}

type myProviderBody struct {
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Concurrency int    `json:"concurrency"`
}

func (a *App) handleMyProviderSave(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	ctx := r.Context()
	if !a.userProvidersAllowed(ctx, u.ID) {
		writeErr(w, http.StatusForbidden, "an admin has not turned on providers of your own")
		return
	}
	var b myProviderBody
	if !decode(w, r, &b) {
		return
	}
	var p store.Provider
	editing := chi.URLParam(r, "id") != ""
	if editing {
		cur, ok := a.myProvider(w, r)
		if !ok {
			return
		}
		p = *cur
	} else {
		own, _ := a.Store.ListUserProviders(ctx, u.ID)
		if len(own) >= maxUserProviders {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("you can have %d providers; remove one first", maxUserProviders))
			return
		}
		p = store.Provider{OwnerID: u.ID, Kind: provider.KindOpenAI, Enabled: true}
	}
	b.Name = strings.TrimSpace(b.Name)
	if !editing || b.Name != "" {
		if !userProviderNameRe.MatchString(b.Name) {
			writeErr(w, http.StatusBadRequest, "a name is 1 to 40 letters, digits, dots, dashes or underscores, starting with a letter or digit")
			return
		}
		p.DisplayName = b.Name
		p.Name = userProviderName(u.ID, b.Name)
	}
	if !editing || b.BaseURL != "" {
		base, err := a.checkUserProviderURL(b.BaseURL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		p.BaseURL = base
	}
	if b.Concurrency < 0 || b.Concurrency > 8 {
		writeErr(w, http.StatusBadRequest, "concurrency is 1 to 8")
		return
	}
	p.Concurrency = b.Concurrency
	if p.Concurrency == 0 {
		p.Concurrency = userProviderConcurrency
	}
	if b.APIKey != "" || !editing {
		if b.APIKey == "" {
			writeErr(w, http.StatusBadRequest, "an API key is required")
			return
		}
		if len(b.APIKey) > 4096 || strings.ContainsAny(b.APIKey, "\r\n") {
			writeErr(w, http.StatusBadRequest, "that does not look like an API key")
			return
		}
		enc, err := a.Secrets.Vault.Seal([]byte(b.APIKey), "provider:"+p.Name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not store the key")
			return
		}
		p.APIKeyEnc = enc
	} else if editing && p.APIKeyEnc != nil {
		// the name is part of what the key is sealed to: a rename re-seals it
		old, _ := a.Store.GetProvider(ctx, p.ID)
		if old != nil && old.Name != p.Name {
			pt, err := a.Secrets.Vault.Open(old.APIKeyEnc, "provider:"+old.Name)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "could not read the stored key")
				return
			}
			enc, err := a.Secrets.Vault.Seal(pt, "provider:"+p.Name)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "could not store the key")
				return
			}
			p.APIKeyEnc = enc
		}
	}
	// it must work before it is kept: this also proves the address is a public one
	if err := a.checkUserProvider(ctx, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := a.Store.UpsertProvider(ctx, p)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "you already have a provider with that name")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not save")
		return
	}
	p.ID = id
	if !editing {
		_, _ = a.refreshUserProviderModels(ctx, &p)
	}
	act := "userprovider.add"
	if editing {
		act = "userprovider.update"
	}
	_, _ = a.Store.AppendAudit(ctx, "user:"+itoa(u.ID), act, "provider:"+itoa(id), p.DisplayName+" "+p.BaseURL)
	saved, _ := a.Store.GetProvider(ctx, id)
	if saved == nil {
		saved = &p
	}
	writeJSON(w, http.StatusOK, a.providerJSON(ctx, *saved))
}

// checkUserProvider connects to the service with the stored key and reports what is wrong in words
// that never include the key.
func (a *App) checkUserProvider(ctx context.Context, p *store.Provider) error {
	pr, err := a.userProviderFor(p)
	if err != nil {
		return errors.New("could not read the stored key")
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := pr.ListModels(cctx); err != nil {
		switch {
		case errors.Is(err, provider.ErrAuth):
			return errors.New("the service did not accept the API key")
		case strings.Contains(err.Error(), "not allowed"):
			return errors.New("that address is not allowed; only public services can be added here")
		}
		return errors.New("could not reach the service at that address, or it is not an OpenAI-compatible API")
	}
	return nil
}

// refreshUserProviderModels lists the service's models. New ones start switched off: the user turns
// on the ones to use, and each gets a default window until they set one.
func (a *App) refreshUserProviderModels(ctx context.Context, p *store.Provider) (int, error) {
	pr, err := a.userProviderFor(p)
	if err != nil {
		return 0, err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ms, err := pr.ListModels(cctx)
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		if _, err := a.Store.UpsertModel(ctx, store.Model{ProviderID: p.ID, Name: m.Name, ContextOverride: DefaultUserModelWindow, ToolsSupported: -1, SeenAt: a.Now()}); err != nil {
			return 0, err
		}
	}
	return len(ms), nil
}

// DefaultUserModelWindow is the window assumed for a hosted model until its owner sets one.
const DefaultUserModelWindow = 32768

func (a *App) handleMyProviderRefresh(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	p, ok := a.myProvider(w, r)
	if !ok {
		return
	}
	if !a.userProvidersAllowed(r.Context(), u.ID) {
		writeErr(w, http.StatusForbidden, "an admin has not turned on providers of your own")
		return
	}
	if _, err := a.refreshUserProviderModels(r.Context(), p); err != nil {
		msg := "could not reach the service"
		if errors.Is(err, provider.ErrAuth) {
			msg = "the service did not accept the API key"
		}
		writeErr(w, http.StatusBadGateway, msg)
		return
	}
	writeJSON(w, http.StatusOK, a.providerJSON(r.Context(), *p))
}

func (a *App) handleMyProviderDelete(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	p, ok := a.myProvider(w, r)
	if !ok {
		return
	}
	a.removeUserProvider(r.Context(), *p, "user:"+itoa(u.ID))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// removeUserProvider deletes a user's provider and stops the sessions running on it.
func (a *App) removeUserProvider(ctx context.Context, p store.Provider, actor string) {
	a.stopUserProviderRuns(ctx, func(x store.Provider) bool { return x.ID == p.ID })
	_ = a.Store.DeleteProvider(ctx, p.ID)
	_, _ = a.Store.AppendAudit(ctx, actor, "userprovider.remove", "provider:"+itoa(p.ID), p.DisplayName)
}

func (a *App) handleMyModelSet(w http.ResponseWriter, r *http.Request) {
	u := a.user(r)
	p, ok := a.myProvider(w, r)
	if !ok {
		return
	}
	if !a.userProvidersAllowed(r.Context(), u.ID) {
		writeErr(w, http.StatusForbidden, "an admin has not turned on providers of your own")
		return
	}
	var b struct {
		Enabled *bool `json:"enabled"`
		Context *int  `json:"context"`
	}
	if !decode(w, r, &b) {
		return
	}
	mid, _ := strconv.ParseInt(chi.URLParam(r, "model"), 10, 64)
	m, err := a.Store.GetModel(r.Context(), mid)
	if err != nil || m.ProviderID != p.ID {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if b.Context != nil {
		if *b.Context < 1024 || *b.Context > 4_000_000 {
			writeErr(w, http.StatusBadRequest, "the context window is between 1024 and 4,000,000 tokens")
			return
		}
		_ = a.Store.SetModelContext(r.Context(), m.ID, *b.Context)
	}
	if b.Enabled != nil {
		_ = a.Store.SetModelEnabled(r.Context(), m.ID, *b.Enabled)
	}
	writeJSON(w, http.StatusOK, a.providerJSON(r.Context(), *p))
}

// myModels adds the signed-in user's own providers' enabled models to the list of models they can
// start a session with, each marked external.
func (a *App) myModels(ctx context.Context, u *store.User) []map[string]any {
	out := []map[string]any{}
	if !a.userProvidersAllowed(ctx, u.ID) {
		return out
	}
	provs, _ := a.Store.ListUserProviders(ctx, u.ID)
	pt := a.probes(ctx)
	for _, p := range provs {
		if !p.Enabled {
			continue
		}
		ms, _ := a.Store.ListModels(ctx, p.ID)
		for _, m := range ms {
			if !m.Enabled {
				continue
			}
			out = append(out, map[string]any{"id": userModelID(p.DisplayName, m.Name), "name": m.Name, "provider": "@" + p.DisplayName,
				"tools": pt.toolsUsable(m), "recommended": false, "external": true})
		}
	}
	return out
}

// resolveUserModel finds "@name/model" among the user's own providers, if they may use them.
func (a *App) resolveUserModel(ctx context.Context, u *store.User, id string) (int64, string, bool) {
	pn, mn, ok := strings.Cut(id, "/")
	if !ok || !strings.HasPrefix(pn, "@") || !a.userProvidersAllowed(ctx, u.ID) {
		return 0, "", false
	}
	provs, _ := a.Store.ListUserProviders(ctx, u.ID)
	for _, p := range provs {
		if p.DisplayName != strings.TrimPrefix(pn, "@") || !p.Enabled {
			continue
		}
		ms, _ := a.Store.ListModels(ctx, p.ID)
		for _, m := range ms {
			if m.Name == mn && m.Enabled {
				return p.ID, mn, true
			}
		}
	}
	return 0, "", false
}

// ---- the admin's view ----

func (a *App) adminUserProviders(w http.ResponseWriter, r *http.Request) {
	ps, _ := a.Store.ListUserProviders(r.Context(), 0)
	users, _ := a.Store.ListUsers(r.Context())
	email := map[int64]string{}
	for _, u := range users {
		email[u.ID] = u.Email
	}
	out := []map[string]any{}
	for _, p := range ps {
		ms, _ := a.Store.ListModels(r.Context(), p.ID)
		on := 0
		for _, m := range ms {
			if m.Enabled {
				on++
			}
		}
		// names and addresses only: the key is never in a response
		out = append(out, map[string]any{"id": p.ID, "user_id": p.OwnerID, "user": email[p.OwnerID], "name": p.DisplayName, "base_url": p.BaseURL, "models": len(ms), "models_on": on,
			"allowed": a.userProvidersAllowed(r.Context(), p.OwnerID)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) adminUserProviderRemove(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.GetProvider(r.Context(), pathID(r))
	if err != nil || p.OwnerID == 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	a.removeUserProvider(r.Context(), *p, a.actor(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// adminUserProvidersSwitch sets one user's override: "on", "off", or "" to follow the instance switch.
func (a *App) adminUserProvidersSwitch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Value != "on" && b.Value != "off" && b.Value != "" {
		writeErr(w, http.StatusBadRequest, `the value is "on", "off" or empty (follow the instance setting)`)
		return
	}
	uid := pathID(r)
	if _, err := a.Store.GetUser(r.Context(), uid); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	_ = a.Store.SetSetting(r.Context(), settingUserProvidersUser+itoa(uid), b.Value)
	_, _ = a.Store.AppendAudit(r.Context(), a.actor(r), "userprovider.switch", "user:"+itoa(uid), b.Value)
	if !a.userProvidersAllowed(r.Context(), uid) {
		a.stopUserProviderRuns(r.Context(), func(p store.Provider) bool { return p.OwnerID == uid })
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": b.Value, "allowed": a.userProvidersAllowed(r.Context(), uid)})
}

// stopRunsOfDisallowed stops sessions on user providers whose owner may no longer use them; called
// after the instance switch changes.
func (a *App) stopRunsOfDisallowed(ctx context.Context) {
	a.stopUserProviderRuns(ctx, func(p store.Provider) bool { return !a.userProvidersAllowed(ctx, p.OwnerID) })
}
