// SPDX-License-Identifier: GPL-2.0-or-later

package testenv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// GiteaServer is a real Gitea running in a container for one test.
type GiteaServer struct {
	URL   string // http://127.0.0.1:PORT
	User  string
	Pass  string
	Token string // an access token for User with every scope
}

// Gitea starts a real Gitea server in a container (removed afterwards) with one admin user and a
// token. It skips, saying NOT TESTED, if Docker or the image is unavailable.
func Gitea(t testing.TB) *GiteaServer {
	t.Helper()
	Docker(t)
	name := fmt.Sprintf("syntaxis-test-gitea-%d", time.Now().UnixNano()%1e9)
	// pick a free port first: Gitea writes its own address into the clone URLs it returns
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	root := fmt.Sprintf("http://127.0.0.1:%d", port)
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:3000", port),
		"-e", "GITEA__server__ROOT_URL="+root, "-e", "GITEA__security__INSTALL_LOCK=true", "-e", "GITEA__database__DB_TYPE=sqlite3",
		"-e", "GITEA__service__DISABLE_REGISTRATION=true", "gitea/gitea:1.22").CombinedOutput()
	if err != nil {
		NotTested(t, "the Gitea image could not be started (no network to pull it?): "+strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	g := &GiteaServer{URL: root, User: "tester", Pass: "Passw0rd-test-1"}
	hc := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(120 * time.Second)
	for {
		r, err := hc.Get(g.URL + "/api/healthz")
		if err == nil {
			_ = r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("Gitea did not become ready")
		}
		time.Sleep(time.Second)
	}
	if b, err := exec.Command("docker", "exec", "-u", "git", name, "gitea", "admin", "user", "create", "--admin", "--username", g.User,
		"--password", g.Pass, "--email", "t@example.com", "--must-change-password=false").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	body, _ := json.Marshal(map[string]any{"name": "t", "scopes": []string{"all"}})
	req, _ := http.NewRequest("POST", g.URL+"/api/v1/users/"+g.User+"/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(g.User, g.Pass)
	r, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var tk struct {
		Sha1 string `json:"sha1"`
	}
	_ = json.NewDecoder(r.Body).Decode(&tk)
	if r.StatusCode != 201 || tk.Sha1 == "" {
		t.Fatalf("token: %d", r.StatusCode)
	}
	g.Token = tk.Sha1
	return g
}

// Do calls Gitea's API with the token, for setting up what a test then checks.
func (g *GiteaServer) Do(t testing.TB, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, g.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+g.Token)
	r, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r.Body)
	return r.StatusCode, buf.Bytes()
}
