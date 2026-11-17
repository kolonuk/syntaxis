// SPDX-License-Identifier: GPL-2.0-or-later

// Package watch checks outside sources for something new (a release, a tag, a feed entry, a changed
// page) so an automation can start when it happens. Every request is made by the Syntaxis server on
// behalf of a user, to an address the user typed, so it is made through a client that refuses
// anything that is not the public internet.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kolonuk/syntaxis/internal/safehttp"
)

// MaxBody is how much of a response is read.
const MaxBody = 1 << 20

// NewClient returns an HTTP client for watching public sources: it refuses private, loopback,
// link-local, metadata and other blocked addresses at connection time (see package safehttp).
func NewClient() *http.Client { return safehttp.NewClient(25 * time.Second) }

// get fetches a URL (http or https only) and returns at most MaxBody bytes.
func get(ctx context.Context, hc *http.Client, url string, header map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, 0, errors.New("only http and https addresses can be watched")
	}
	if req.URL.User != nil {
		return nil, 0, errors.New("put no password in the address")
	}
	req.Header.Set("User-Agent", "syntaxis-watch")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("could not fetch it: %w", unwrapURL(err))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		return b, resp.StatusCode, fmt.Errorf("the server answered HTTP %d", resp.StatusCode)
	}
	return b, resp.StatusCode, nil
}

func unwrapURL(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) && ue.Unwrap() != nil {
		return ue.Unwrap()
	}
	return err
}
