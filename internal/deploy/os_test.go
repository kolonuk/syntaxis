// SPDX-License-Identifier: GPL-2.0-or-later

// Package deploy tests the host-side scripts (installer helpers) that are not Go code.
package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func detect(t *testing.T, osRelease, arch string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(f, []byte(osRelease), 0o600); err != nil {
		t.Fatal(err)
	}
	// VERSION, NAME and ID are variables in os-release: sourcing the helper must not change the caller's own
	cmd := exec.Command("sh", "-c", `VERSION=mine; ID=mine; . ./scripts/os.sh; echo "$OS_ID $OS_FAMILY $OS_CODENAME $CPU_ARCH $(docker_repo_id) $VERSION $ID"`)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "OS_RELEASE_FILE="+f, "SYNTAXIS_TEST_ARCH="+arch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestOSDetection(t *testing.T) {
	cases := []struct{ name, release, arch, want string }{
		{"debian", "ID=debian\nVERSION_CODENAME=bookworm\n", "x86_64", "debian debian bookworm amd64 debian"},
		{"ubuntu", "ID=ubuntu\nID_LIKE=debian\nVERSION_CODENAME=noble\nUBUNTU_CODENAME=noble\n", "x86_64", "ubuntu debian noble amd64 ubuntu"},
		{"linux mint uses the Ubuntu base", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_CODENAME=wilma\nUBUNTU_CODENAME=noble\n", "x86_64", "linuxmint debian noble amd64 debian"},
		{"raspberry pi os (64-bit)", "ID=debian\nVERSION_CODENAME=bookworm\n", "aarch64", "debian debian bookworm arm64 debian"},
		{"raspbian (older)", "ID=raspbian\nID_LIKE=debian\nVERSION_CODENAME=bullseye\n", "armv7l", "raspbian debian bullseye unsupported debian"},
		{"almalinux", "ID=almalinux\nID_LIKE=\"rhel centos fedora\"\n", "x86_64", "almalinux rhel  amd64 centos"},
		{"rocky linux", "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n", "aarch64", "rocky rhel  arm64 centos"},
		{"centos stream", "ID=centos\nID_LIKE=\"rhel fedora\"\n", "x86_64", "centos rhel  amd64 centos"},
		{"fedora", "ID=fedora\n", "x86_64", "fedora rhel  amd64 fedora"},
		{"unknown", "ID=plan9\n", "riscv64", "plan9 unknown  unsupported centos"},
	}
	for _, c := range cases {
		if got := detect(t, c.release, c.arch); got != c.want+" mine mine" {
			t.Errorf("%s: got %q want %q", c.name, got, c.want+" mine mine")
		}
	}
}

func TestGitCredentialHelperOnlyAnswersForTheMatchingHost(t *testing.T) {
	run := func(host string, env ...string) string {
		cmd := exec.Command("sh", "images/sandbox/syntaxis-git-credential", "get")
		cmd.Dir = "../.."
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...) // nothing inherited: only what the test sets
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	all := []string{"GITHUB_TOKEN=gh-secret", "GITLAB_TOKEN=gl-secret", "GITLAB_HOST=https://gitlab.example.com/", "GITEA_TOKEN=gt-secret", "GITEA_URL=https://gitea.example.org"}
	if got := run("github.com", all...); got != "username=x-access-token\npassword=gh-secret\n" {
		t.Fatalf("github: %q", got)
	}
	if got := run("gitlab.example.com", all...); got != "username=oauth2\npassword=gl-secret\n" {
		t.Fatalf("gitlab: %q", got)
	}
	if got := run("gitea.example.org", all...); got != "username=token\npassword=gt-secret\n" {
		t.Fatalf("gitea: %q", got)
	}
	// every other host gets nothing: a token must never reach a host it was not made for
	for _, h := range []string{"evil.example.net", "github.com.evil.example.net", "gitlab.com", "example.org"} {
		if got := run(h, all...); got != "" {
			t.Errorf("%s was given credentials: %q", h, got)
		}
	}
	if got := run("github.com"); got != "" {
		t.Errorf("no token set, yet: %q", got)
	}
	// gitlab.com is the default when no address is configured
	if got := run("gitlab.com", "GITLAB_TOKEN=gl-secret"); got != "username=oauth2\npassword=gl-secret\n" {
		t.Errorf("default gitlab: %q", got)
	}
}
