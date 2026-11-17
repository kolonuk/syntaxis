// SPDX-License-Identifier: GPL-2.0-or-later

// Package connections lets each user connect their own accounts at other services (GitHub,
// GitLab, Gitea, Cloudflare, AWS, Azure, Google Cloud). A connection is the user's own
// credentials: they are kept in that user's secrets vault under the environment variable names
// the services' own tools read (GITHUB_TOKEN, AWS_ACCESS_KEY_ID and so on), so the CLIs and SDKs
// in the user's sandbox work as they would on a laptop. Nothing here is shared between users.
//
// Syntaxis itself uses a connection only to check it, to list and create repositories, and to
// open pull requests. The credentials never go to the model.
package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/safehttp"
)

// Field is one credential or setting a connection needs.
type Field struct {
	Name     string `json:"name"` // the environment variable it is stored and injected as
	Label    string `json:"label"`
	Hint     string `json:"hint,omitempty"`
	Secret   bool   `json:"secret"` // hidden in the form and never shown again
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
	// URL marks the address of a self-hosted service. The server calls it, so it is checked
	// (CheckBaseURL) and reached only through the safe client (see package safehttp).
	URL bool `json:"url,omitempty"`
}

// Kind describes a service.
type Kind struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Guide  string  `json:"guide"` // how to create a least-privilege credential
	Fields []Field `json:"fields"`
	Git    bool    `json:"git"` // can list/create repositories and open pull requests
}

// Creds are the field values, keyed by Field.Name.
type Creds map[string]string

// Identity says who the credentials belong to, from the service itself.
type Identity struct {
	Account string `json:"account"`
	Detail  string `json:"detail,omitempty"`
}

// Repo is a repository at a git service.
type Repo struct {
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	WebURL        string `json:"web_url"`
}

// NewRepo is a repository to create under the user's own account.
type NewRepo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Private     bool   `json:"private"`
}

// PullRequest is what the agent asks to open.
type PullRequest struct {
	Repo  string // full name
	Title string
	Body  string
	Head  string // branch with the changes
	Base  string // branch to merge into (empty = the repository's default)
}

// Provider is one service.
type Provider interface {
	Kind() Kind
	// Test checks the credentials against the service and says whose they are.
	Test(ctx context.Context, c Creds) (Identity, error)
}

// GitProvider is a provider with repositories.
type GitProvider interface {
	Provider
	ListRepos(ctx context.Context, c Creds, query string) ([]Repo, error)
	CreateRepo(ctx context.Context, c Creds, n NewRepo) (Repo, error)
	// OpenPR opens a pull request (merge request) and returns its address.
	OpenPR(ctx context.Context, c Creds, pr PullRequest) (string, error)
	// ReadFile returns a file from the repository at a branch or tag ("" = the default branch), or
	// ErrFileNotFound. It is for small configuration files, not for cloning.
	ReadFile(ctx context.Context, c Creds, repo, ref, path string) ([]byte, error)
}

// ErrFileNotFound means the repository has no such file.
var ErrFileNotFound = errors.New("the repository has no such file")

// maxFile is the largest file ReadFile returns.
const maxFile = 256 << 10

// callRaw fetches a file's bytes. A 404 is ErrFileNotFound.
func callRaw(ctx context.Context, hc *http.Client, url string, header map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the service: %w", errors.Unwrap(err))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFile+1))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrFileNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrBadCredentials, resp.StatusCode)
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("the service answered HTTP %d: %s", resp.StatusCode, snippet(b))
	case len(b) > maxFile:
		return nil, errors.New("the file is too large")
	}
	return b, nil
}

// safeRepoPath accepts a relative path made of safe characters, so it can go into a URL.
func safeRepoPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./", r)) {
			return false
		}
	}
	return true
}

func safeRef(r string) bool {
	if r == "" {
		return true
	}
	for _, c := range r {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_./", c)) {
			return false
		}
	}
	return !strings.Contains(r, "..")
}

// ErrBadCredentials means the service refused the credentials.
var ErrBadCredentials = errors.New("the service did not accept these credentials")

// Registry holds the providers, with an HTTP client that tests can replace.
type Registry struct {
	byID  map[string]Provider
	order []string
}

// New returns the registry of all supported services. hc must refuse internal addresses (the
// server calls addresses users type); nil uses safehttp with no private ranges allowed.
func New(hc *http.Client) *Registry {
	if hc == nil {
		hc = safehttp.NewClient(15 * time.Second)
	}
	r := &Registry{byID: map[string]Provider{}}
	for _, p := range []Provider{
		&GitHub{HC: hc}, &GitLab{HC: hc}, &Gitea{HC: hc}, &Cloudflare{HC: hc}, &AWS{HC: hc}, &Azure{HC: hc}, &GCP{HC: hc}, &IFTTT{HC: hc},
	} {
		r.byID[p.Kind().ID] = p
		r.order = append(r.order, p.Kind().ID)
	}
	return r
}

// Set adds or replaces a provider (used by tests to point a service at a fake).
func (r *Registry) Set(p Provider) {
	id := p.Kind().ID
	if _, ok := r.byID[id]; !ok {
		r.order = append(r.order, id)
	}
	r.byID[id] = p
}

// Kinds lists the services in display order.
func (r *Registry) Kinds() []Kind {
	var out []Kind
	for _, id := range r.order {
		out = append(out, r.byID[id].Kind())
	}
	return out
}

// Get returns a provider.
func (r *Registry) Get(id string) (Provider, bool) { p, ok := r.byID[id]; return p, ok }

// Git returns a provider that has repositories.
func (r *Registry) Git(id string) (GitProvider, bool) {
	p, ok := r.byID[id]
	g, ok2 := p.(GitProvider)
	return g, ok && ok2
}

// Validate checks that the required fields are present and have no control characters.
func Validate(k Kind, c Creds) error {
	for _, f := range k.Fields {
		v := c[f.Name]
		if f.Required && strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", f.Label)
		}
		if f.Name != "GCP_SERVICE_ACCOUNT_JSON" && strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("%s must be a single line", f.Label)
		}
		if len(v) > 16<<10 {
			return fmt.Errorf("%s is too long", f.Label)
		}
		if f.URL && v != "" {
			if err := safehttp.CheckBaseURL(v); err != nil {
				return fmt.Errorf("%s: %w", f.Label, err)
			}
		}
	}
	return nil
}

// ---- small HTTP helpers shared by the providers ----

const maxResponse = 2 << 20

func call(ctx context.Context, hc *http.Client, method, url string, header map[string]string, body []byte, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		// the URL can carry a host the user typed; the error text never carries a credential
		return 0, fmt.Errorf("could not reach the service: %w", errors.Unwrap(err))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, fmt.Errorf("%w (HTTP %d: %s)", ErrBadCredentials, resp.StatusCode, snippet(b))
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("the service answered HTTP %d: %s", resp.StatusCode, snippet(b))
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("unexpected answer from the service: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func jsonBody(v any) []byte { b, _ := json.Marshal(v); return b }

func bearer(tok string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + tok, "Accept": "application/json", "Content-Type": "application/json"}
}

type errString string

func (e errString) Error() string { return string(e) }

func httpErr(status int, body []byte) error {
	return fmt.Errorf("the service answered HTTP %d: %s", status, snippet(body))
}
