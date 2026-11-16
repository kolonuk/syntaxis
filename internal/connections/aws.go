// SPDX-License-Identifier: GPL-2.0-or-later

package connections

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS connects a user's own access key (preferably temporary credentials with a session token).
type AWS struct {
	HC       *http.Client
	Endpoint string // default https://sts.amazonaws.com/
	Now      func() time.Time
}

// Kind implements Provider.
func (a *AWS) Kind() Kind {
	return Kind{ID: "aws", Title: "Amazon Web Services",
		Guide: "Prefer temporary credentials (aws sts get-session-token or an assumed role) so that they expire; otherwise create an IAM user for this purpose with only the permissions you need and a budget alert on the account. Never use the root account's keys.",
		Fields: []Field{
			{Name: "AWS_ACCESS_KEY_ID", Label: "Access key ID", Required: true},
			{Name: "AWS_SECRET_ACCESS_KEY", Label: "Secret access key", Secret: true, Required: true},
			{Name: "AWS_SESSION_TOKEN", Label: "Session token", Secret: true, Hint: "Only for temporary credentials"},
			{Name: "AWS_DEFAULT_REGION", Label: "Default region", Hint: "For example eu-west-2", Default: "us-east-1"},
		}}
}

// Test implements Provider: STS GetCallerIdentity, which any valid key may call.
func (a *AWS) Test(ctx context.Context, c Creds) (Identity, error) {
	ep := a.Endpoint
	if ep == "" {
		ep = "https://sts.amazonaws.com/"
	}
	body := []byte("Action=GetCallerIdentity&Version=2011-06-15")
	req, err := http.NewRequestWithContext(ctx, "POST", ep, strings.NewReader(string(body)))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	signV4(req, body, c["AWS_ACCESS_KEY_ID"], c["AWS_SECRET_ACCESS_KEY"], c["AWS_SESSION_TOKEN"], "us-east-1", "sts", now())
	var out struct {
		Account string `xml:"GetCallerIdentityResult>Account"`
		Arn     string `xml:"GetCallerIdentityResult>Arn"`
	}
	resp, err := a.HC.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer resp.Body.Close()
	b := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for len(b) < maxResponse {
		n, rerr := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	if resp.StatusCode == 403 || resp.StatusCode == 401 {
		return Identity{}, ErrBadCredentials
	}
	if resp.StatusCode/100 != 2 {
		return Identity{}, httpErr(resp.StatusCode, b)
	}
	if err := xml.Unmarshal(b, &out); err != nil || out.Account == "" {
		return Identity{}, httpErr(resp.StatusCode, b)
	}
	return Identity{Account: out.Account, Detail: out.Arn}, nil
}

// signV4 adds an AWS Signature Version 4 Authorization header to req.
func signV4(req *http.Request, body []byte, accessKey, secret, token, region, service string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if token != "" {
		req.Header.Set("X-Amz-Security-Token", token)
	}
	host := req.URL.Host
	req.Header.Set("Host", host)

	// signed headers: host, x-amz-* and content-type when present
	hdrs := map[string]string{"host": host, "x-amz-date": amzDate}
	if token != "" {
		hdrs["x-amz-security-token"] = token
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		hdrs["content-type"] = ct
	}
	var names []string
	for k := range hdrs {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + strings.TrimSpace(hdrs[k]) + "\n")
	}
	signed := strings.Join(names, ";")

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	q := req.URL.Query()
	var qs []string
	for k, vs := range q {
		for _, v := range vs {
			qs = append(qs, awsEscape(k)+"="+awsEscape(v))
		}
	}
	sort.Strings(qs)
	canonical := strings.Join([]string{req.Method, path, strings.Join(qs, "&"), canonHeaders.String(), signed, hexSHA(body)}, "\n")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA([]byte(canonical))
	k := hmacSHA([]byte("AWS4"+secret), date)
	k = hmacSHA(k, region)
	k = hmacSHA(k, service)
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func awsEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func hexSHA(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func hmacSHA(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
