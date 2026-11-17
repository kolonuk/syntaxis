// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/safehttp"
)

// Web search goes through a self-hosted SearXNG instance that an admin names in the search_url
// setting. The server makes the request (the sandbox's own network rules stay as they are), with the
// address-checking client: a SearXNG on the LAN needs SYNTAXIS_CONNECTIONS_ALLOW_PRIVATE, like a
// self-hosted GitLab does.

func validSearchURL(v string) bool { return safehttp.CheckBaseURL(v) == nil }

const maxSearchBody = 1 << 20

// searxResponse is the part of SearXNG's JSON reply that is used.
type searxResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// parseSearx reads a SearXNG reply, keeping only web addresses.
func parseSearx(body []byte, limit int) ([]agent.SearchResult, error) {
	var r searxResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, errors.New("the search server did not return JSON; enable the json format in its settings (search.formats)")
	}
	var out []agent.SearchResult
	for _, x := range r.Results {
		u, err := url.Parse(x.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		out = append(out, agent.SearchResult{Title: clip(x.Title, 200), URL: x.URL, Snippet: clip(x.Content, 400)})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// searchFunc returns the web search for the agent, or nil when no search server is set.
func (a *App) searchFunc(ctx context.Context) func(context.Context, string, int) ([]agent.SearchResult, error) {
	base := strings.TrimRight(a.setting(ctx, SettingSearchURL, ""), "/")
	if base == "" || !validSearchURL(base) {
		return nil
	}
	client := a.ConnectionsClient()
	return func(c context.Context, query string, limit int) ([]agent.SearchResult, error) {
		c, cancel := context.WithTimeout(c, 20*time.Second)
		defer cancel()
		q := url.Values{"q": {query}, "format": {"json"}, "safesearch": {"1"}}
		req, err := http.NewRequestWithContext(c, http.MethodGet, base+"/search?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "syntaxis")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("the search server could not be reached: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxSearchBody))
		if resp.StatusCode == http.StatusForbidden {
			return nil, errors.New("the search server refused the request; enable the json format in its settings (search.formats)")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the search server answered %d", resp.StatusCode)
		}
		return parseSearx(body, limit)
	}
}
