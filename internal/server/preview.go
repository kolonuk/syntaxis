// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// previewPortMin keeps previews away from privileged services.
const previewPortMin = 1024

// handlePreview reverse-proxies /preview/<session>/<port>/... to a dev server
// running inside the caller's own sandbox. Only the session owner can reach it
// (the same cookie/CSRF-safe GET rules apply), and only to ports >= 1024 on
// that sandbox's address on the sandbox network.
func (a *App) handlePreview(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	port, err := strconv.Atoi(chiParam(r, "port"))
	if err != nil || port < previewPortMin || port > 65535 {
		writeErr(w, http.StatusBadRequest, "invalid port")
		return
	}
	if a.Sandbox == nil || s.SandboxID == "" {
		writeErr(w, http.StatusNotFound, "this session has no running sandbox")
		return
	}
	ip, err := a.Sandbox.RT.ContainerIP(r.Context(), s.SandboxID, a.Sandbox.Cfg.Network)
	if err != nil {
		writeErr(w, http.StatusNotFound, "this session has no running sandbox")
		return
	}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(ip, strconv.Itoa(port))}
	prefix := "/preview/" + s.ID + "/" + strconv.Itoa(port)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(pr.In.URL.Path, prefix), "/")
			pr.Out.URL.RawPath = ""
			pr.Out.Header.Del("Cookie") // never forward Syntaxis session cookies to user code
			pr.Out.Header.Del("X-CSRF-Token")
		},
		ModifyResponse: func(resp *http.Response) error {
			// the previewed app is untrusted content: sandbox it and keep it out of our origin's privileges
			resp.Header.Del("Set-Cookie")
			resp.Header.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups")
			resp.Header.Set("X-Content-Type-Options", "nosniff")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeErr(w, http.StatusBadGateway, "nothing is listening on that port in your sandbox")
		},
	}
	w.Header().Del("Content-Security-Policy") // replaced by the sandboxing policy set in ModifyResponse
	proxy.ServeHTTP(w, r)
}
