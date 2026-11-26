// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"errors"
	"net/http"
	"regexp"
)

// an account ID is 32 hex characters; checked because it goes into a URL path
var cloudflareAccountID = regexp.MustCompile(`^[0-9a-f]{32}$`)

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
		Guide: "In the Cloudflare dashboard open My Profile, API Tokens, and create a custom token with only what you need (for example Workers Scripts: Edit and Zone: DNS: Edit for the one zone), and an expiry. A token made under an account (Manage Account, Account API Tokens) needs the account ID, shown on the dashboard's overview page; a token made under My Profile does not.",
		Fields: []Field{
			{Name: "CLOUDFLARE_API_TOKEN", Label: "API token", Secret: true, Required: true},
			{Name: "CLOUDFLARE_ACCOUNT_ID", Label: "Account ID", Hint: "Needed for an account-owned token; also used by Wrangler"},
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
	// a token made under My Profile is verified as the user's; one made under an account only
	// verifies through that account
	verify := c.base() + "/user/tokens/verify"
	if id := cr["CLOUDFLARE_ACCOUNT_ID"]; id != "" {
		if !cloudflareAccountID.MatchString(id) {
			return Identity{}, errors.New("the account ID is 32 hexadecimal characters")
		}
		verify = c.base() + "/accounts/" + id + "/tokens/verify"
	}
	if _, err := call(ctx, c.HC, "GET", verify, bearer(cr["CLOUDFLARE_API_TOKEN"]), nil, &out); err != nil {
		return Identity{}, err
	}
	if !out.Success || out.Result.Status != "active" {
		return Identity{}, ErrBadCredentials
	}
	return Identity{Account: "token " + out.Result.ID, Detail: "active"}, nil
}
