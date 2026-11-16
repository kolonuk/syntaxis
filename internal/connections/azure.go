// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Azure connects a user's own service principal.
type Azure struct {
	HC        *http.Client
	LoginBase string // default https://login.microsoftonline.com
	ARMBase   string // default https://management.azure.com
}

var guidRe = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

func (a *Azure) login() string {
	if a.LoginBase != "" {
		return a.LoginBase
	}
	return "https://login.microsoftonline.com"
}

func (a *Azure) arm() string {
	if a.ARMBase != "" {
		return a.ARMBase
	}
	return "https://management.azure.com"
}

// Kind implements Provider.
func (a *Azure) Kind() Kind {
	return Kind{ID: "azure", Title: "Microsoft Azure",
		Guide: "Create a service principal for this purpose (az ad sp create-for-rbac) scoped to one resource group or subscription with only the role you need (for example Contributor on one resource group), and set a short secret lifetime. Add a budget alert on the subscription.",
		Fields: []Field{
			{Name: "AZURE_TENANT_ID", Label: "Tenant ID", Required: true},
			{Name: "AZURE_CLIENT_ID", Label: "Client (application) ID", Required: true},
			{Name: "AZURE_CLIENT_SECRET", Label: "Client secret", Secret: true, Required: true},
			{Name: "AZURE_SUBSCRIPTION_ID", Label: "Subscription ID", Required: true},
		}}
}

// Test implements Provider: it asks for a token, then reads the subscription.
func (a *Azure) Test(ctx context.Context, c Creds) (Identity, error) {
	if !guidRe.MatchString(c["AZURE_TENANT_ID"]) || !guidRe.MatchString(c["AZURE_SUBSCRIPTION_ID"]) {
		return Identity{}, errString("the tenant and subscription IDs should look like 00000000-0000-0000-0000-000000000000")
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c["AZURE_CLIENT_ID"]}, "client_secret": {c["AZURE_CLIENT_SECRET"]}, "scope": {"https://management.azure.com/.default"}}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if _, err := call(ctx, a.HC, "POST", a.login()+"/"+c["AZURE_TENANT_ID"]+"/oauth2/v2.0/token",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Accept": "application/json"}, []byte(form.Encode()), &tok); err != nil {
		return Identity{}, err
	}
	if tok.AccessToken == "" {
		return Identity{}, ErrBadCredentials
	}
	var sub struct {
		DisplayName string `json:"displayName"`
		State       string `json:"state"`
	}
	if _, err := call(ctx, a.HC, "GET", a.arm()+"/subscriptions/"+c["AZURE_SUBSCRIPTION_ID"]+"?api-version=2022-12-01", bearer(tok.AccessToken), nil, &sub); err != nil {
		return Identity{}, err
	}
	return Identity{Account: strings.TrimSpace(sub.DisplayName), Detail: "subscription " + sub.State}, nil
}
