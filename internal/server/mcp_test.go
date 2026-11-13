// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/mcp"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/sandbox"
	"github.com/kolonuk/syntaxis/internal/store"
)

func echoServer(in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		var res any
		switch req.Method {
		case "initialize":
			res = map[string]any{"protocolVersion": mcp.ProtocolVersion, "capabilities": map[string]any{}}
		case "tools/list":
			res = map[string]any{"tools": []map[string]any{{"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}}}}
		case "tools/call":
			res = map[string]any{"content": []map[string]any{{"type": "text", "text": "echoed by the MCP server"}}}
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": res})
		fmt.Fprintf(out, "%s\n", b)
	}
}

func TestMCPAdminApproveAndAgentUsesIt(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	fake := sandbox.NewFake()
	fake.AttachHandlers = map[string]func(io.Reader, io.Writer){sandbox.DefaultConfig().Image: echoServer}
	e.app.Sandbox = sandbox.NewManager(fake, sandbox.DefaultConfig(), nil)
	e.app.MCPSource = mcp.Local{Entries: []mcp.Entry{{Name: "com.example/echo", Description: "echo server", Version: "1.0.0",
		Packages: []mcp.Package{{RegistryType: "npm", Identifier: "echo-mcp", Version: "1.0.0", Transport: "stdio", Env: []mcp.EnvVar{{Name: "API_KEY", Required: true, Secret: true}}}}}}}

	usr := e.userSession("a@example.com")
	for _, p := range []string{"/api/mcp/search", "/api/mcp/approve", "/api/mcp/approved", "/api/secrets/instance"} {
		if code, _, _ := usr.do("POST", p, map[string]string{}); code == 200 {
			t.Fatalf("chat user reached admin MCP route %s", p)
		}
	}
	adm := e.makeAdminSession()
	_, res, raw := adm.do("GET", "/api/mcp/search?q=echo", nil)
	if servers, _ := res["servers"].([]any); len(servers) != 1 || !strings.Contains(string(raw), "executes third-party code") {
		t.Fatalf("search with review: %s", raw)
	}
	// approval refuses an unmapped required credential, and a secret that does not exist
	if code, _, _ := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "com.example/echo", "package": 0}); code != 400 {
		t.Fatalf("approved without credentials: %d", code)
	}
	if code, _, _ := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "com.example/echo", "package": 0, "env_secrets": map[string]string{"API_KEY": "ECHO_KEY"}}); code != 400 {
		t.Fatalf("approved with a missing instance secret: %d", code)
	}
	if code, _, _ := adm.do("PUT", "/api/secrets/ECHO_KEY", map[string]string{"value": "instance-secret-value"}); code != 200 {
		t.Fatal("put instance secret")
	}
	if code, _, raw := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "com.example/echo", "package": 0, "env_secrets": map[string]string{"API_KEY": "ECHO_KEY"}}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _, _ := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "com.example/unknown", "package": 0}); code != 404 {
		t.Fatal("unknown server approved")
	}

	call := provider.ToolCall{ID: "c1", Name: "mcp__com_example_echo__echo", Arguments: json.RawMessage(`{"text":"hi"}`)}
	sp := &stubProvider{replies: []*provider.ChatResult{{ToolCalls: []provider.ToolCall{call}}, {Content: "used the server"}}}
	e.app.NewProvider = func(*store.Provider) (provider.Provider, error) { return sp, nil }
	e.app.RunAgent = e.app.RunAgentFunc(Wiring{})
	_, s, _ := usr.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	usr.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "use echo"})
	e.app.Runs.Get(id).Wait()
	offered := false
	for _, d := range sp.reqs[0].Tools {
		if d.Name == "mcp__com_example_echo__echo" {
			offered = true
		}
	}
	if !offered {
		t.Fatal("the approved MCP tool was not offered to the model")
	}
	_, _, hist := usr.do("GET", "/api/sessions/"+id+"/messages", nil)
	if !strings.Contains(string(hist), "echoed by the MCP server") || !strings.Contains(string(hist), "untrusted_content") {
		t.Fatalf("history: %s", hist)
	}
	for _, rq := range sp.reqs {
		for _, m := range rq.Messages {
			if strings.Contains(m.Content, "instance-secret-value") {
				t.Fatal("MCP credential leaked to the model")
			}
		}
	}
	if fake.Count() > 1 { // only the session sandbox may remain; the MCP container must be destroyed with the run
		t.Fatalf("MCP server container left running: %d containers", fake.Count())
	}
	// users can switch a server off for a session
	usr.do("PUT", "/api/sessions/"+id+"/mcp", map[string]any{"disabled": []string{"com.example/echo"}})
	sp.reqs, sp.replies = nil, nil
	usr.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "again"})
	e.app.Runs.Get(id).Wait()
	for _, d := range sp.reqs[0].Tools {
		if strings.HasPrefix(d.Name, "mcp__") {
			t.Fatal("a disabled MCP server's tools were still offered")
		}
	}
	// revoke
	if code, _, _ := adm.do("DELETE", "/api/mcp/approved?name=com.example/echo", nil); code != 200 {
		t.Fatal("revoke")
	}
	_ = context.Background()
}
