// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"net/http"
)

// Cloudflare connects a user's own API token.
type Cloudflare struct {
	HC      *http.Client
	APIBase string // default https://api.cloudflare.com/client/v4
}

func (c *Cloudflare) base() string {
	if c.APIBase != "" {
		return c.APIBase
	}
	return "https://api.cloudflare.com/client/v4"
}

// Kind implements Provider.
func (c *Cloudflare) Kind() Kind {
	return Kind{ID: "cloudflare", Title: "Cloudflare",
		Guide: "In the Cloudflare dashboard open My Profile, API Tokens, and create a custom token with only what you need (for example Workers Scripts: Edit and Zone: DNS: Edit for the one zone), and an expiry. The account ID is optional; it is shown on the dashboard's overview page.",
		Fields: []Field{
			{Name: "CLOUDFLARE_API_TOKEN", Label: "API token", Secret: true, Required: true},
			{Name: "CLOUDFLARE_ACCOUNT_ID", Label: "Account ID", Hint: "Optional; used by Wrangler"},
		}}
}

// Test implements Provider.
func (c *Cloudflare) Test(ctx context.Context, cr Creds) (Identity, error) {
	var out struct {
		Success bool `json:"success"`
		Result  struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"result"`
	}
	if _, err := call(ctx, c.HC, "GET", c.base()+"/user/tokens/verify", bearer(cr["CLOUDFLARE_API_TOKEN"]), nil, &out); err != nil {
		return Identity{}, err
	}
	if !out.Success || out.Result.Status != "active" {
		return Identity{}, ErrBadCredentials
	}
	return Identity{Account: "token " + out.Result.ID, Detail: "active"}, nil
}
