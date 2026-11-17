// SPDX-License-Identifier: GPL-2.0-or-later

// Package devcontainer reads the part of a repository's devcontainer.json that Syntaxis honours,
// and maps it onto the environments an admin has approved. A repository never gets to pick an
// arbitrary image: its request is matched against the admin's list, and anything else is ignored
// with a reason the user can see. See https://containers.dev/implementors/json_reference/.
package devcontainer

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Paths is where a repository may keep the file, in the order the specification tries them.
var Paths = []string{".devcontainer/devcontainer.json", ".devcontainer.json"}

// Config is what is used from the file. Features, ports, mounts, users and extensions are ignored:
// the sandbox is always the same hardened, non-root, no-published-ports container.
type Config struct {
	Name         string
	Image        string
	Dockerfile   bool // asks to be built from a Dockerfile or Compose file (not supported)
	ContainerEnv map[string]string
	PostCreate   string // one shell command line; "" if none
	Template     string // customizations.syntaxis.template: an approved environment named directly
}

// ErrNotDevcontainer means the text is not a devcontainer.json at all.
var ErrNotDevcontainer = errors.New("not a devcontainer.json")

// Parse reads a devcontainer.json. The format allows comments and trailing commas.
func Parse(b []byte) (*Config, error) {
	var raw struct {
		Name          string            `json:"name"`
		Image         string            `json:"image"`
		Build         json.RawMessage   `json:"build"`
		DockerFile    string            `json:"dockerFile"`
		DockerCompose json.RawMessage   `json:"dockerComposeFile"`
		ContainerEnv  map[string]string `json:"containerEnv"`
		PostCreate    json.RawMessage   `json:"postCreateCommand"`
		Customization struct {
			Syntaxis struct {
				Template string `json:"template"`
			} `json:"syntaxis"`
		} `json:"customizations"`
	}
	if err := json.Unmarshal(stripJSONC(b), &raw); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotDevcontainer, err)
	}
	c := &Config{Name: raw.Name, Image: strings.TrimSpace(raw.Image), ContainerEnv: map[string]string{}, Template: raw.Customization.Syntaxis.Template}
	c.Dockerfile = raw.DockerFile != "" || len(raw.Build) > 0 || len(raw.DockerCompose) > 0
	for k, v := range raw.ContainerEnv {
		if validEnvName(k) && !strings.ContainsAny(v, "\x00\n\r") {
			c.ContainerEnv[k] = v
		}
	}
	c.PostCreate = command(raw.PostCreate)
	return c, nil
}

func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// command turns the three forms of postCreateCommand (a string, an argument list, or named
// commands run in turn) into one shell line.
func command(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		q := make([]string, len(list))
		for i, a := range list {
			q[i] = shellQuote(a)
		}
		return strings.Join(q, " ")
	}
	var named map[string]json.RawMessage
	if json.Unmarshal(raw, &named) == nil {
		keys := make([]string, 0, len(named))
		for k := range named {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			if c := command(named[k]); c != "" {
				parts = append(parts, "("+c+")")
			}
		}
		return strings.Join(parts, " && ")
	}
	return ""
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Template is an environment the admin has approved: a prepared image, and the images a repository
// may name in its devcontainer.json to get it (matched by prefix, ignoring the tag).
type Template struct {
	Name        string
	Image       string
	Matches     []string
	Description string
}

// Choose says which approved environment a repository's devcontainer.json asks for. ok is false
// when it asks for nothing Syntaxis can honour; reason then says why, for the user.
func Choose(c *Config, ts []Template) (t Template, reason string, ok bool) {
	if c == nil {
		return Template{}, "", false
	}
	if c.Template != "" {
		for _, x := range ts {
			if x.Name == c.Template {
				return x, "the repository names the " + x.Name + " environment", true
			}
		}
		return Template{}, "the repository names the " + c.Template + " environment, which is not available here", false
	}
	if c.Dockerfile && c.Image == "" {
		return Template{}, "the repository builds its own container from a Dockerfile, which is not supported", false
	}
	if c.Image == "" {
		return Template{}, "", false
	}
	want := repoOf(c.Image)
	for _, x := range ts {
		for _, m := range x.Matches {
			if m = strings.TrimSpace(m); m != "" && (want == repoOf(m) || strings.HasPrefix(want, strings.TrimRight(repoOf(m), "/")+"/")) {
				return x, "the repository's devcontainer.json asks for " + c.Image, true
			}
		}
	}
	return Template{}, "the repository's devcontainer.json asks for " + c.Image + ", which is not one of the approved environments", false
}

// repoOf drops the tag or digest from an image reference.
func repoOf(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image
}

// stripJSONC removes // and /* */ comments and trailing commas, leaving strings alone.
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	// trailing commas before } or ]
	res := make([]byte, 0, len(out))
	inStr, esc = false, false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inStr {
			res = append(res, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
		}
		if c == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		res = append(res, c)
	}
	return res
}
