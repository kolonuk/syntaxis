// SPDX-License-Identifier: GPL-2.0-or-later

package environments

import (
	"os"
	"path/filepath"
	"testing"
)

// Every Dockerfile in images/templates is offered, and nothing is offered without one.
func TestCatalogMatchesTheDockerfiles(t *testing.T) {
	dirs, err := os.ReadDir(filepath.Join("..", "..", "images", "templates"))
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, d := range dirs {
		if d.IsDir() {
			if _, err := os.Stat(filepath.Join("..", "..", "images", "templates", d.Name(), "Dockerfile")); err != nil {
				t.Errorf("%s has no Dockerfile", d.Name())
			}
			onDisk[d.Name()] = true
		}
	}
	seen := map[string]bool{}
	for _, e := range Catalog {
		if !ValidName(e.Name) || seen[e.Name] {
			t.Errorf("bad or repeated name %q", e.Name)
		}
		seen[e.Name] = true
		if !onDisk[e.Name] {
			t.Errorf("%s is in the catalog but has no images/templates/%s", e.Name, e.Name)
		}
		if e.Adds == "" || e.SizeGB <= 0 || e.Minutes == "" {
			t.Errorf("%s is incomplete: %+v", e.Name, e)
		}
	}
	for name := range onDisk {
		if !seen[name] {
			t.Errorf("images/templates/%s is not in the catalog", name)
		}
	}
}
