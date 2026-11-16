// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// GCP connects a user's own service account key and project.
type GCP struct {
	HC       *http.Client
	TokenURL string // default: the token_uri in the key (Google's own)
	CRMBase  string // default https://cloudresourcemanager.googleapis.com
	Now      func() time.Time
}

// Kind implements Provider.
func (g *GCP) Kind() Kind {
	return Kind{ID: "gcp", Title: "Google Cloud",
		Guide: "In the Google Cloud console create a service account in your own project for this purpose, give it only the roles you need, create a JSON key and paste its contents below. Set a budget alert on the project, and delete the key when you are finished. Short-lived credentials are better where you can use them.",
		Fields: []Field{
			{Name: "GCP_SERVICE_ACCOUNT_JSON", Label: "Service account key (JSON)", Secret: true, Required: true},
			{Name: "GOOGLE_CLOUD_PROJECT", Label: "Project ID", Required: true},
		}}
}

type saKey struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func parseKey(js string) (*saKey, *rsa.PrivateKey, error) {
	var k saKey
	if err := json.Unmarshal([]byte(js), &k); err != nil {
		return nil, nil, errors.New("the key is not valid JSON")
	}
	if k.Type != "service_account" || k.ClientEmail == "" || k.PrivateKey == "" {
		return nil, nil, errors.New("this does not look like a service account key")
	}
	blk, _ := pem.Decode([]byte(k.PrivateKey))
	if blk == nil {
		return nil, nil, errors.New("the key's private key could not be read")
	}
	pk, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, nil, errors.New("the key's private key could not be read")
	}
	rk, ok := pk.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("the key must be an RSA key")
	}
	return &k, rk, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Test implements Provider: sign a token request with the key, then read the project.
func (g *GCP) Test(ctx context.Context, c Creds) (Identity, error) {
	k, rk, err := parseKey(c["GCP_SERVICE_ACCOUNT_JSON"])
	if err != nil {
		return Identity{}, err
	}
	tokenURL := g.TokenURL
	if tokenURL == "" {
		tokenURL = k.TokenURI
	}
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}
	now := time.Now
	if g.Now != nil {
		now = g.Now
	}
	iat := now().Unix()
	claims, _ := json.Marshal(map[string]any{"iss": k.ClientEmail, "scope": "https://www.googleapis.com/auth/cloud-platform", "aud": tokenURL, "iat": iat, "exp": iat + 3600})
	signing := b64([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + b64(claims)
	h := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rk, crypto.SHA256, h[:])
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {signing + "." + b64(sig)}}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if _, err := call(ctx, g.HC, "POST", tokenURL, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, []byte(form.Encode()), &tok); err != nil {
		return Identity{}, err
	}
	if tok.AccessToken == "" {
		return Identity{}, ErrBadCredentials
	}
	crm := g.CRMBase
	if crm == "" {
		crm = "https://cloudresourcemanager.googleapis.com"
	}
	var p struct {
		ProjectID string `json:"projectId"`
		Name      string `json:"name"`
	}
	if _, err := call(ctx, g.HC, "GET", crm+"/v1/projects/"+url.PathEscape(c["GOOGLE_CLOUD_PROJECT"]), bearer(tok.AccessToken), nil, &p); err != nil {
		return Identity{}, err
	}
	return Identity{Account: k.ClientEmail, Detail: "project " + p.ProjectID}, nil
}
