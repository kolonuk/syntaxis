// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"errors"
	"os"
	"testing"
)

func TestDetectContainer(t *testing.T) {
	none := func(string) (os.FileInfo, error) { return nil, errors.New("no") }
	noFile := func(string) ([]byte, error) { return nil, errors.New("no") }
	noEnv := func(string) string { return "" }

	if detectContainer(none, noFile, noEnv) {
		t.Fatal("a plain host must not be reported as a container")
	}
	if !detectContainer(func(p string) (os.FileInfo, error) {
		if p == "/.dockerenv" {
			return nil, nil
		}
		return nil, errors.New("no")
	}, noFile, noEnv) {
		t.Fatal("/.dockerenv means a container")
	}
	if !detectContainer(none, noFile, func(k string) string {
		if k == "KUBERNETES_SERVICE_HOST" {
			return "10.0.0.1"
		}
		return ""
	}) {
		t.Fatal("a Kubernetes pod is a container")
	}
	cg := func(string) ([]byte, error) { return []byte("0::/kubepods/burstable/pod1\n"), nil }
	if !detectContainer(none, cg, noEnv) {
		t.Fatal("a kubepods cgroup is a container")
	}
	host := func(string) ([]byte, error) { return []byte("0::/init.scope\n"), nil }
	if detectContainer(none, host, noEnv) {
		t.Fatal("a host cgroup is not a container")
	}
}
