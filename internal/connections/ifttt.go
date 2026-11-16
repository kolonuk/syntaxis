// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"net/http"
	"net/url"
)

// IFTTT connects a user's own Webhooks key, so an automation can trigger one of their IFTTT
// applets when it finishes. (To start an automation from IFTTT, use the automation's webhook address
// in an IFTTT "Make a web request" action; that needs no connection.)
type IFTTT struct {
	HC   *http.Client
	Base string // default https://maker.ifttt.com
}

// Kind implements Provider.
func (i *IFTTT) Kind() Kind {
	return Kind{ID: "ifttt", Title: "IFTTT",
		Guide:  "In IFTTT open Webhooks, then Documentation: your key is the last part of the address shown there. Anyone with the key can trigger your applets, so keep it private.",
		Fields: []Field{{Name: "IFTTT_WEBHOOK_KEY", Label: "Webhooks key", Secret: true, Required: true}}}
}

// Test implements Provider: it triggers a test event, which succeeds for a valid key.
func (i *IFTTT) Test(ctx context.Context, c Creds) (Identity, error) {
	base := i.Base
	if base == "" {
		base = "https://maker.ifttt.com"
	}
	_, err := call(ctx, i.HC, "POST", base+"/trigger/syntaxis_connection_test/with/key/"+url.PathEscape(c["IFTTT_WEBHOOK_KEY"]), map[string]string{"Content-Type": "application/json"}, []byte(`{"value1":"connection test"}`), nil)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Account: "Webhooks key accepted", Detail: "a test event named syntaxis_connection_test was sent"}, nil
}
