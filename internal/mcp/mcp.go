// SPDX-License-Identifier: GPL-2.0-or-later

// Package mcp is the admin catalogue for Model Context Protocol servers: it
// browses a registry (the official MCP Registry by default, source is
// pluggable), summarises what an entry would do for review, records admin
// approvals, and builds the hardened container spec to run an approved server
// in its own gVisor sandbox. Running the stdio protocol bridge is not
// implemented yet; see docs/integrations.md.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

// DefaultRegistry is the official MCP Registry. Verified at build time:
// GET /v0/servers?search=&limit=&cursor=&version=latest returns
// {"servers":[{"server":{...},"_meta":{...}}],"metadata":{"nextCursor","count"}}.
const DefaultRegistry = "https://registry.modelcontextprotocol.io"

// EnvVar is a variable a server wants.
type EnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Default     string `json:"default"`
}

// Package is one way to run a server.
type Package struct {
	RegistryType string   `json:"registryType"` // npm | pypi | oci | nuget | mcpb
	Identifier   string   `json:"identifier"`
	Version      string   `json:"version"`
	RuntimeHint  string   `json:"runtimeHint"`
	Transport    string   `json:"transport"` // stdio | streamable-http | sse
	Env          []EnvVar `json:"env"`
}

// Entry is a catalogue entry normalised from any source.
type Entry struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Version     string    `json:"version"`
	Repository  string    `json:"repository"`
	Packages    []Package `json:"packages"`
	Remotes     []string  `json:"remotes"`
	Status      string    `json:"status"`
}

// Source is a pluggable catalogue.
type Source interface {
	Search(ctx context.Context, query, cursor string, limit int) (entries []Entry, next string, err error)
}

// Registry is the MCP Registry client.
type Registry struct {
	BaseURL string
	Client  *http.Client
}

type regResponse struct {
	Servers []struct {
		Server struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Version     string `json:"version"`
			Repository  struct {
				URL string `json:"url"`
			} `json:"repository"`
			Packages []struct {
				RegistryType string `json:"registryType"`
				Identifier   string `json:"identifier"`
				Version      string `json:"version"`
				RuntimeHint  string `json:"runtimeHint"`
				Transport    struct {
					Type string `json:"type"`
				} `json:"transport"`
				EnvironmentVariables []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
					IsRequired  bool   `json:"isRequired"`
					IsSecret    bool   `json:"isSecret"`
					Default     string `json:"default"`
				} `json:"environmentVariables"`
			} `json:"packages"`
			Remotes []struct {
				URL string `json:"url"`
			} `json:"remotes"`
		} `json:"server"`
		Meta map[string]struct {
			Status string `json:"status"`
		} `json:"_meta"`
	} `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

// Search queries the registry (latest versions only).
func (r *Registry) Search(ctx context.Context, query, cursor string, limit int) ([]Entry, string, error) {
	base := r.BaseURL
	if base == "" {
		base = DefaultRegistry
	}
	cl := r.Client
	if cl == nil {
		cl = &http.Client{Timeout: 20 * time.Second}
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	q := url.Values{"version": {"latest"}, "limit": {fmt.Sprint(limit)}}
	if query != "" {
		q.Set("search", query)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v0/servers?"+q.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("registry returned HTTP %d", resp.StatusCode)
	}
	var rr regResponse
	if err := json.NewDecoder(limitBody(resp)).Decode(&rr); err != nil {
		return nil, "", fmt.Errorf("unexpected registry response: %w", err)
	}
	var out []Entry
	for _, s := range rr.Servers {
		e := Entry{Name: s.Server.Name, Title: s.Server.Title, Description: s.Server.Description, Version: s.Server.Version, Repository: s.Server.Repository.URL}
		for _, m := range s.Meta {
			e.Status = m.Status
		}
		for _, p := range s.Server.Packages {
			pk := Package{RegistryType: p.RegistryType, Identifier: p.Identifier, Version: p.Version, RuntimeHint: p.RuntimeHint, Transport: p.Transport.Type}
			for _, ev := range p.EnvironmentVariables {
				pk.Env = append(pk.Env, EnvVar{Name: ev.Name, Description: ev.Description, Required: ev.IsRequired, Secret: ev.IsSecret, Default: ev.Default})
			}
			e.Packages = append(e.Packages, pk)
		}
		for _, rm := range s.Server.Remotes {
			e.Remotes = append(e.Remotes, rm.URL)
		}
		out = append(out, e)
	}
	return out, rr.Metadata.NextCursor, nil
}

func limitBody(r *http.Response) *limitedBody { return &limitedBody{r: r.Body, n: 8 << 20} }

type limitedBody struct {
	r interface{ Read([]byte) (int, error) }
	n int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errors.New("registry response too large")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// Local is a curated, offline catalogue.
type Local struct{ Entries []Entry }

// Search filters the curated list.
func (l Local) Search(_ context.Context, query, _ string, limit int) ([]Entry, string, error) {
	var out []Entry
	q := strings.ToLower(query)
	for _, e := range l.Entries {
		if q == "" || strings.Contains(strings.ToLower(e.Name+" "+e.Description), q) {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, "", nil
}

// Review is what the admin sees before approving.
type Review struct {
	Name            string   `json:"name"`
	Source          string   `json:"source"`
	Version         string   `json:"version"`
	RunsAs          string   `json:"runs_as"`
	NeedsCredential []string `json:"needs_credentials"`
	Capabilities    []string `json:"capabilities"`
	Warnings        []string `json:"warnings"`
	Pinnable        bool     `json:"pinnable"`
}

var exactVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+([\-+][\w.\-+]+)?$`)

// Summarise builds a review for one package (index pkg) of an entry.
func Summarise(e Entry, pkg int) (Review, error) {
	if pkg < 0 || pkg >= len(e.Packages) {
		if len(e.Remotes) > 0 {
			return Review{Name: e.Name, Version: e.Version, RunsAs: "remote (hosted by a third party)", Source: e.Repository,
				Warnings: []string{"Remote servers run outside your infrastructure and receive everything the agent sends them. Syntaxis only runs servers in its own gVisor containers, so remote entries cannot be approved."}}, errors.New("mcp: remote servers cannot be run in a sandbox")
		}
		return Review{}, errors.New("mcp: no such package")
	}
	p := e.Packages[pkg]
	r := Review{Name: e.Name, Source: e.Repository, Version: p.Version, Pinnable: exactVersion.MatchString(p.Version) || strings.Contains(p.Identifier, "@sha256:")}
	r.RunsAs = fmt.Sprintf("%s package %s@%s over %s", p.RegistryType, p.Identifier, p.Version, p.Transport)
	for _, ev := range p.Env {
		if ev.Required || ev.Secret {
			r.NeedsCredential = append(r.NeedsCredential, ev.Name)
		}
	}
	sort.Strings(r.NeedsCredential)
	r.Capabilities = append(r.Capabilities, "executes third-party code in its own sandbox", "its tool output is untrusted content for the agent")
	if len(r.NeedsCredential) > 0 {
		r.Capabilities = append(r.Capabilities, "receives the credentials you map to: "+strings.Join(r.NeedsCredential, ", "))
	}
	r.Capabilities = append(r.Capabilities, "uses the sandbox egress policy for network access")
	if p.Transport != "stdio" {
		r.Warnings = append(r.Warnings, "only stdio servers can be run by Syntaxis; transport is "+p.Transport)
	}
	if !r.Pinnable {
		r.Warnings = append(r.Warnings, "version is not an exact release; it cannot be pinned")
	}
	if e.Status == "deprecated" || e.Status == "deleted" {
		r.Warnings = append(r.Warnings, "the registry marks this server as "+e.Status)
	}
	switch p.RegistryType {
	case "npm", "pypi", "oci":
	default:
		r.Warnings = append(r.Warnings, "package type "+p.RegistryType+" is not supported")
	}
	return r, nil
}

// Approval records an admin decision to run a server.
type Approval struct {
	Name       string            `json:"name"`
	Package    Package           `json:"package"`
	EnvSecrets map[string]string `json:"env_secrets"` // server env var -> instance secret name
	ApprovedBy string            `json:"approved_by"`
	At         time.Time         `json:"at"`
	Enabled    bool              `json:"enabled"`
}

// ErrNotApprovable explains why an entry cannot be approved.
var ErrNotApprovable = errors.New("mcp: entry cannot be approved")

// Approvals persists approvals in the settings table.
type Approvals struct {
	Store store.Store
	Now   func() time.Time
}

const settingKey = "mcp_approvals"

func (a *Approvals) load(ctx context.Context) (map[string]Approval, error) {
	raw, err := a.Store.GetSetting(ctx, settingKey, "{}")
	if err != nil {
		return nil, err
	}
	m := map[string]Approval{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (a *Approvals) save(ctx context.Context, m map[string]Approval) error {
	b, _ := json.Marshal(m)
	return a.Store.SetSetting(ctx, settingKey, string(b))
}

// Approve records approval after enforcing the safety rules: exact version
// pin, supported package type, stdio transport, and every required credential mapped.
func (a *Approvals) Approve(ctx context.Context, admin string, e Entry, pkg int, envSecrets map[string]string) error {
	rv, err := Summarise(e, pkg)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotApprovable, err)
	}
	p := e.Packages[pkg]
	if !rv.Pinnable || p.Transport != "stdio" || (p.RegistryType != "npm" && p.RegistryType != "pypi" && p.RegistryType != "oci") {
		return fmt.Errorf("%w: %s", ErrNotApprovable, strings.Join(rv.Warnings, "; "))
	}
	for _, ev := range p.Env {
		if ev.Required && envSecrets[ev.Name] == "" && ev.Default == "" {
			return fmt.Errorf("%w: required variable %s has no secret mapped", ErrNotApprovable, ev.Name)
		}
	}
	m, err := a.load(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	m[e.Name] = Approval{Name: e.Name, Package: p, EnvSecrets: envSecrets, ApprovedBy: admin, At: now, Enabled: true}
	if err := a.save(ctx, m); err != nil {
		return err
	}
	_, _ = a.Store.AppendAudit(ctx, admin, "mcp.approve", e.Name, p.Identifier+"@"+p.Version)
	return nil
}

// List returns approvals by name.
func (a *Approvals) List(ctx context.Context) ([]Approval, error) {
	m, err := a.load(ctx)
	if err != nil {
		return nil, err
	}
	var out []Approval
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Revoke removes an approval.
func (a *Approvals) Revoke(ctx context.Context, admin, name string) error {
	m, err := a.load(ctx)
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return store.ErrNotFound
	}
	delete(m, name)
	_, _ = a.Store.AppendAudit(ctx, admin, "mcp.revoke", name, "")
	return a.save(ctx, m)
}

var safeIdent = regexp.MustCompile(`^[@A-Za-z0-9][A-Za-z0-9@/._:\-]*$`)

// ContainerSpec builds the hardened spec to run an approved server: its own
// gVisor container, no workspace mount, no host mounts, only the secrets mapped
// to it, the sandbox network (egress policy applies), pinned version.
func ContainerSpec(cfg sandbox.Config, ap Approval, secretValues map[string]string) (sandbox.Spec, error) {
	p := ap.Package
	if !safeIdent.MatchString(p.Identifier) || !exactVersion.MatchString(p.Version) && !strings.Contains(p.Identifier, "@sha256:") {
		return sandbox.Spec{}, fmt.Errorf("%w: unsafe or unpinned package", ErrNotApprovable)
	}
	var cmd []string
	image := cfg.Image
	switch p.RegistryType {
	case "npm":
		cmd = []string{"npx", "--yes", p.Identifier + "@" + p.Version}
	case "pypi":
		cmd = []string{"uvx", p.Identifier + "==" + p.Version}
	case "oci":
		image, cmd = p.Identifier, nil
	default:
		return sandbox.Spec{}, fmt.Errorf("%w: unsupported package type", ErrNotApprovable)
	}
	env := map[string]string{}
	for k, secretName := range ap.EnvSecrets {
		v, ok := secretValues[secretName]
		if !ok {
			return sandbox.Spec{}, fmt.Errorf("mcp: secret %q is not available", secretName)
		}
		env[k] = v
	}
	s, err := sandbox.BuildSpec(cfg, 0, "mcp-"+sanitize(ap.Name), env, false)
	if err != nil {
		return s, err
	}
	s.Image, s.Cmd = image, cmd
	s.Mounts = nil // an MCP server never sees any workspace
	s.Name = "syntaxis-mcp-" + sanitize(ap.Name)
	s.Labels["io.syntaxis.mcp"] = ap.Name
	return s, sandbox.Validate(s, 0)
}

func sanitize(s string) string {
	return regexp.MustCompile(`[^a-zA-Z0-9_.-]`).ReplaceAllString(s, "_")
}
