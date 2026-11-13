// SPDX-License-Identifier: GPL-2.0-or-later

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// fakeServer implements enough of an MCP stdio server for tests.
func fakeServer(in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
			continue // notification
		}
		reply := func(result any) {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
			fmt.Fprintf(out, "%s\n", b)
		}
		fmt.Fprintln(out, "server log line that is not JSON-RPC") // must be ignored
		switch req.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fake"}})
		case "tools/list":
			var p struct{ Cursor string }
			_ = json.Unmarshal(req.Params, &p)
			if p.Cursor == "" {
				reply(map[string]any{"tools": []map[string]any{{"name": "echo", "description": "echo text", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}}}, "nextCursor": "p2"})
			} else {
				reply(map[string]any{"tools": []map[string]any{{"name": "write.file", "description": "writes", "inputSchema": map[string]any{"type": "object"}}}})
			}
		case "tools/call":
			var p struct {
				Name      string
				Arguments map[string]any
			}
			_ = json.Unmarshal(req.Params, &p)
			switch p.Name {
			case "echo":
				reply(map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprint("echo: ", p.Arguments["text"])}, {"type": "image", "data": "xx"}}})
			case "write.file":
				reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "disk full"}}, "isError": true})
			default:
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "error": map[string]any{"code": -32601, "message": "no such tool"}})
				fmt.Fprintf(out, "%s\n", b)
			}
		}
	}
}

func pipeClient(h func(io.Reader, io.Writer)) *Client {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { h(inR, outW); _ = outW.Close() }()
	return NewClient(outR, inW)
}

func TestClientHandshakeListCall(t *testing.T) {
	c := pipeClient(fakeServer)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(ctx)
	if err != nil || len(tools) != 2 || !tools[0].Annotations.ReadOnlyHint || tools[1].Name != "write.file" {
		t.Fatalf("pagination: %+v %v", tools, err)
	}
	text, isErr, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || isErr || !strings.Contains(text, "echo: hi") || !strings.Contains(text, "[image content omitted]") {
		t.Fatalf("%q %v %v", text, isErr, err)
	}
	if _, isErr, _ := c.CallTool(ctx, "write.file", nil); !isErr {
		t.Fatal("isError flag lost")
	}
	if _, _, err := c.CallTool(ctx, "nope", nil); err == nil || !strings.Contains(err.Error(), "no such tool") {
		t.Fatalf("%v", err)
	}
}

func TestClientServerDiesAndTimeouts(t *testing.T) {
	c := pipeClient(func(in io.Reader, out io.Writer) { io.Copy(io.Discard, io.LimitReader(in, 10)) }) // reads, never answers, then exits
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := c.Initialize(ctx); err == nil {
		t.Fatal("unanswered request must time out")
	}
	c2 := pipeClient(func(in io.Reader, out io.Writer) {}) // exits immediately
	time.Sleep(50 * time.Millisecond)
	if err := c2.Initialize(context.Background()); err == nil {
		t.Fatal("dead server accepted a request")
	}
}

func TestClientIgnoresOversizedGarbage(t *testing.T) {
	c := pipeClient(func(in io.Reader, out io.Writer) {
		sc := bufio.NewScanner(in)
		sc.Scan()
		fmt.Fprintln(out, strings.Repeat("x", 10000)) // not JSON
		fmt.Fprintln(out, `{"jsonrpc":"2.0","method":"notifications/message","params":{}}`)
		fmt.Fprintln(out, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		io.Copy(io.Discard, in) // keep reading like a real server
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
}

func bridgeFixture(t *testing.T) (*Bridge, *sandbox.Fake, Approval) {
	f := sandbox.NewFake()
	cfg := sandbox.DefaultConfig()
	f.AttachHandlers = map[string]func(io.Reader, io.Writer){cfg.Image: fakeServer}
	ap := Approval{Name: "com.example/fs", Package: Package{RegistryType: "npm", Identifier: "example-fs-mcp", Version: "1.2.3", Transport: "stdio"}, EnvSecrets: map[string]string{"TOKEN": "MCP_TOKEN"}}
	return NewBridge(f, cfg), f, ap
}

func TestBridgeStartsServerInGVisorAndExposesTools(t *testing.T) {
	b, f, ap := bridgeFixture(t)
	ctx := context.Background()
	if err := b.Start(ctx, ap, map[string]string{"MCP_TOKEN": "s3cret-value"}); err != nil {
		t.Fatal(err)
	}
	if f.Count() != 1 {
		t.Fatal("container not created")
	}
	for _, c := range f.Containers {
		for _, m := range c.Spec.Mounts {
			if m.Volume != sandbox.DNSVolume {
				t.Fatalf("an MCP container got a mount other than the resolver config: %+v", m)
			}
		}
		if c.Spec.Runtime != "runsc" || !c.Spec.OpenStdin || !c.Info.Running {
			t.Fatalf("mcp server container not isolated: %+v", c.Spec)
		}
		if !strings.Contains(strings.Join(c.Spec.Env, " "), "TOKEN=s3cret-value") {
			t.Fatal("mapped secret missing")
		}
	}
	defs := b.Defs()
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
		if !strings.Contains(d.Description, "[MCP server com.example/fs]") {
			t.Fatalf("tool must say where it comes from: %q", d.Description)
		}
	}
	if !names["mcp__com_example_fs__echo"] || !names["mcp__com_example_fs__write_file"] || len(defs) != 2 {
		t.Fatalf("%v", names)
	}
	if !b.Has("mcp__com_example_fs__echo") || b.Has("run_shell") || !IsMCPTool("mcp__x__y") || IsMCPTool("git") {
		t.Fatal("namespace checks")
	}
	if !b.ReadOnly("mcp__com_example_fs__echo") || b.ReadOnly("mcp__com_example_fs__write_file") {
		t.Fatal("read-only hint")
	}
	text, isErr, err := b.Call(ctx, "mcp__com_example_fs__echo", json.RawMessage(`{"text":"yo"}`))
	if err != nil || isErr || !strings.Contains(text, "echo: yo") {
		t.Fatalf("%q %v %v", text, isErr, err)
	}
	if _, _, err := b.Call(ctx, "mcp__com_example_fs__missing", nil); err == nil {
		t.Fatal("unknown tool")
	}
	b.Stop(ctx)
	if f.Count() != 0 || len(b.Defs()) != 0 {
		t.Fatal("servers not destroyed on stop")
	}
}

func TestBridgeFailsClosed(t *testing.T) {
	b, f, ap := bridgeFixture(t)
	ctx := context.Background()
	// missing secret
	if err := b.Start(ctx, ap, nil); err == nil {
		t.Fatal("started without the mapped secret")
	}
	// server that never speaks MCP: start times out and the container is removed
	f.AttachHandlers = map[string]func(io.Reader, io.Writer){sandbox.DefaultConfig().Image: func(in io.Reader, out io.Writer) { io.Copy(io.Discard, in) }}
	ctx2, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	if err := b.Start(ctx2, ap, map[string]string{"MCP_TOKEN": "x"}); err == nil {
		t.Fatal("unresponsive server accepted")
	}
	if f.Count() != 0 {
		t.Fatalf("leaked containers after failed start: %d", f.Count())
	}
	if len(b.Defs()) != 0 {
		t.Fatal("tools registered for a failed server")
	}
}

func TestFullNameSanitised(t *testing.T) {
	if got := FullName("io.github.x/my-server", "do thing!"); got != "mcp__io_github_x_my_server__do_thing_" {
		t.Fatal(got)
	}
}
