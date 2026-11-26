// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"os"
	"strings"
)

// runningInContainer reports whether this process runs inside a container (Docker, Podman or a
// Kubernetes pod). The admin app uses it to warn that an update made by hand is undone the next
// time the container is deployed from its Compose file or manifest.
func runningInContainer() bool {
	return detectContainer(os.Stat, os.ReadFile, os.Getenv)
}

// detectContainer is runningInContainer with its inputs passed in, so it can be tested.
func detectContainer(stat func(string) (os.FileInfo, error), read func(string) ([]byte, error), env func(string) string) bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := stat(marker); err == nil {
			return true
		}
	}
	if env("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	if b, err := read("/proc/1/cgroup"); err == nil {
		for _, hint := range []string{"docker", "kubepods", "containerd", "libpod", "crio"} {
			if strings.Contains(string(b), hint) {
				return true
			}
		}
	}
	return false
}
