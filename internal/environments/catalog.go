// SPDX-License-Identifier: GPL-2.0-or-later

// Package environments lists the example sandbox environments that ship with Syntaxis (the
// Dockerfiles in images/templates). The admin app offers to build the ones that are chosen, on the
// Docker host, and to remove them again after they have gone unused for a while.
package environments

import "regexp"

// Entry is one example environment.
type Entry struct {
	Name    string   `json:"name"`
	Adds    string   `json:"adds"`
	SizeGB  float64  `json:"size_gb"` // disk the image takes, roughly (it shares the default image's layers)
	Minutes string   `json:"minutes"` // how long a build takes, roughly
	Matches []string `json:"matches"` // devcontainer images that map to it
}

// Catalog is every example environment, in the order the admin app lists them.
var Catalog = []Entry{
	{"go", "Go tooling: gopls, delve, staticcheck, golangci-lint, govulncheck", 1.7, "3 to 5", []string{"mcr.microsoft.com/devcontainers/go"}},
	{"typescript", "TypeScript: tsc, tsx, ts-node, ESLint, Prettier, Vitest, pnpm, Yarn", 0.2, "2 to 4", []string{"mcr.microsoft.com/devcontainers/typescript-node"}},
	{"rust", "Rust (rustup, cargo)", 1.0, "3 to 6", []string{"mcr.microsoft.com/devcontainers/rust"}},
	{"java", "OpenJDK 17 and Maven", 0.6, "2 to 4", []string{"mcr.microsoft.com/devcontainers/java"}},
	{"kotlin", "Kotlin compiler, OpenJDK 17 and Maven", 0.7, "3 to 5", nil},
	{"dotnet", ".NET 8 SDK", 0.8, "3 to 6", []string{"mcr.microsoft.com/devcontainers/dotnet"}},
	{"ruby", "Ruby and Bundler", 0.4, "2 to 4", []string{"mcr.microsoft.com/devcontainers/ruby"}},
	{"php", "PHP 8 and Composer", 0.4, "2 to 4", []string{"mcr.microsoft.com/devcontainers/php"}},
	{"elixir", "Elixir and Erlang", 0.6, "2 to 4", nil},
	{"haskell", "GHC, Cabal and Stack", 0.9, "3 to 6", []string{"mcr.microsoft.com/devcontainers/haskell"}},
	{"zig", "Zig", 0.4, "2 to 4", nil},
	{"cpp", "clang, CMake, Ninja, gdb and valgrind", 0.7, "2 to 4", []string{"mcr.microsoft.com/devcontainers/cpp"}},
	{"swift", "The Swift toolchain (large)", 5.0, "10 to 20", []string{"swift"}},
	{"browser", "Headless Chromium with Playwright, Puppeteer, axe-core, Jest, Vitest, Mocha and pytest-playwright (large)", 1.5, "8 to 15", nil},
	{"data", "NumPy, pandas, SciPy, scikit-learn, Matplotlib and Jupyter", 1.5, "4 to 8", []string{"mcr.microsoft.com/devcontainers/anaconda"}},
}

// Find returns the entry with this name.
func Find(name string) (Entry, bool) {
	for _, e := range Catalog {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Image is the name a built environment gets on the Docker host (scripts/build-templates.sh).
func Image(name string) string { return "syntaxis-env-" + name + ":1" }

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// ValidName reports whether s could be an environment name (checked again against the catalog).
func ValidName(s string) bool { return nameRe.MatchString(s) }
