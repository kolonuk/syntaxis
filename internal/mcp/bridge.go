// SPDX-License-Identifier: GPL-2.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// Bridge runs approved MCP servers, each in its own gVisor container with its
// stdio attached, and exposes their tools to the agent. One Bridge per agent
// run: servers start with the run and are destroyed when it ends, so state
// never leaks between users or sessions.
type Bridge struct {
	RT  sandbox.Runtime
	Cfg sandbox.Config
	Log func(msg string)

	mu      sync.Mutex
	servers map[string]*running
	tools   map[string]toolRef // full name -> owner
}

type running struct {
	name  string
	id    string
	cl    *Client
	att   *sandbox.Attachment
	tools []Tool
}

type toolRef struct {
	server *running
	tool   Tool
}

// NewBridge returns an empty bridge.
func NewBridge(rt sandbox.Runtime, cfg sandbox.Config) *Bridge {
	return &Bridge{RT: rt, Cfg: cfg, servers: map[string]*running{}, tools: map[string]toolRef{}}
}

var nameSan = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// FullName is the model-facing tool name.
func FullName(server, tool string) string {
	return "mcp__" + strings.Trim(nameSan.ReplaceAllString(server, "_"), "_") + "__" + nameSan.ReplaceAllString(tool, "_")
}

// IsMCPTool reports whether a tool name belongs to the bridge namespace.
func IsMCPTool(name string) bool { return strings.HasPrefix(name, "mcp__") }

// Start launches an approved server and registers its tools.
func (b *Bridge) Start(ctx context.Context, ap Approval, secretValues map[string]string) error {
	spec, err := ContainerSpec(b.Cfg, ap, secretValues)
	if err != nil {
		return err
	}
	spec.OpenStdin = true
	spec.Name += "-" + fmt.Sprint(time.Now().UnixNano()%1e6)
	if len(b.Cfg.DNS) > 0 { // sandboxes mount a shared resolv.conf; make sure it exists
		content, derr := sandbox.ResolvConf(b.Cfg.DNS)
		if derr == nil {
			derr = b.RT.WriteVolumeFile(ctx, sandbox.DNSVolume, "resolv.conf", content, b.Cfg.Image)
		}
		if derr != nil {
			return fmt.Errorf("configure DNS: %w", derr)
		}
	}
	id, err := b.RT.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("create mcp container: %w", err)
	}
	att, err := b.RT.Attach(ctx, id)
	if err != nil {
		_ = b.RT.Remove(ctx, id)
		return fmt.Errorf("attach: %w", err)
	}
	if err := b.RT.Start(ctx, id); err != nil {
		att.Close()
		_ = b.RT.Remove(ctx, id)
		return fmt.Errorf("start mcp container: %w", err)
	}
	cl := NewClient(att.Stdout, att.Stdin)
	sctx, cancel := context.WithTimeout(ctx, 90*time.Second) // npx may need to download the package
	defer cancel()
	fail := func(err error) error {
		if logs, lerr := b.RT.Logs(context.Background(), id, 15); lerr == nil && strings.TrimSpace(logs) != "" {
			if len(logs) > 600 {
				logs = logs[len(logs)-600:]
			}
			err = fmt.Errorf("%w (container output: %s)", err, strings.TrimSpace(logs))
		}
		cl.Close()
		att.Close()
		_ = b.RT.Stop(ctx, id, time.Second)
		_ = b.RT.Remove(ctx, id)
		return err
	}
	if err := cl.Initialize(sctx); err != nil {
		return fail(fmt.Errorf("initialize %s: %w", ap.Name, err))
	}
	tools, err := cl.ListTools(sctx)
	if err != nil {
		return fail(fmt.Errorf("list tools of %s: %w", ap.Name, err))
	}
	r := &running{name: ap.Name, id: id, cl: cl, att: att, tools: tools}
	b.mu.Lock()
	b.servers[ap.Name] = r
	for _, t := range tools {
		b.tools[FullName(ap.Name, t.Name)] = toolRef{r, t}
	}
	b.mu.Unlock()
	return nil
}

// Defs returns the model-facing definitions of every running server's tools.
func (b *Bridge) Defs() []provider.ToolDef {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []provider.ToolDef
	for full, ref := range b.tools {
		schema := ref.tool.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		desc := strings.TrimSpace(ref.tool.Description)
		if len(desc) > 600 {
			desc = desc[:600]
		}
		out = append(out, provider.ToolDef{Name: full, Description: "[MCP server " + ref.server.name + "] " + desc, Parameters: schema})
	}
	return out
}

// Has reports whether a tool is provided by a running server.
func (b *Bridge) Has(full string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.tools[full]
	return ok
}

// ReadOnly reports the server's readOnlyHint for a tool. Untrusted: it only
// ever relaxes approval for hints the admin-approved server itself declares.
func (b *Bridge) ReadOnly(full string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tools[full].tool.Annotations.ReadOnlyHint
}

// Call runs a tool. The result is untrusted content.
func (b *Bridge) Call(ctx context.Context, full string, args json.RawMessage) (string, bool, error) {
	b.mu.Lock()
	ref, ok := b.tools[full]
	b.mu.Unlock()
	if !ok {
		return "", false, fmt.Errorf("unknown MCP tool %s", full)
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	text, isErr, err := ref.server.cl.CallTool(cctx, ref.tool.Name, args)
	if len(text) > 200_000 {
		text = text[:200_000] + "\n[output truncated]"
	}
	return text, isErr, err
}

// Names lists running servers.
func (b *Bridge) Names() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for n := range b.servers {
		out = append(out, n)
	}
	return out
}

// Stop destroys every server container.
func (b *Bridge) Stop(ctx context.Context) {
	b.mu.Lock()
	servers := b.servers
	b.servers, b.tools = map[string]*running{}, map[string]toolRef{}
	b.mu.Unlock()
	for _, s := range servers {
		s.cl.Close()
		s.att.Close()
		_ = b.RT.Stop(ctx, s.id, time.Second)
		_ = b.RT.Remove(ctx, s.id)
	}
}
