// SPDX-License-Identifier: GPL-2.0-or-later

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ProtocolVersion is the MCP revision this client speaks (stdio transport,
// newline-delimited JSON-RPC 2.0).
const ProtocolVersion = "2025-06-18"

// maxLine bounds one JSON-RPC message from an untrusted server.
const maxLine = 4 << 20

// Tool is a tool a server offers.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

// Client speaks MCP over a stdio pipe.
type Client struct {
	w  io.WriteCloser
	mu sync.Mutex // serialises writes

	pmu     sync.Mutex
	pending map[int64]chan rpcResponse
	nextID  int64
	closed  bool
	done    chan struct{}
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Method string `json:"method"`
}

// NewClient starts reading responses from r. Requests are written to w.
func NewClient(r io.Reader, w io.WriteCloser) *Client {
	c := &Client{w: w, pending: map[int64]chan rpcResponse{}, done: make(chan struct{})}
	go c.readLoop(r)
	return c
}

func (c *Client) readLoop(r io.Reader) {
	defer close(c.done)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' { // servers may log non-protocol text to stdout
			continue
		}
		var m rpcResponse
		if json.Unmarshal([]byte(line), &m) != nil || m.Method != "" || len(m.ID) == 0 {
			continue // notifications and server->client requests are ignored (we declare no capabilities)
		}
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			continue
		}
		c.pmu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.pmu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	c.pmu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pmu.Unlock()
}

// ErrClosed means the server went away.
var ErrClosed = errors.New("mcp: server closed the connection")

func (c *Client) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// sendCtx writes in the background so a server that stops reading cannot wedge the caller.
func (c *Client) sendCtx(ctx context.Context, v any) error {
	werr := make(chan error, 1)
	go func() { werr <- c.send(v) }()
	select {
	case err := <-werr:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.pmu.Lock()
	if c.closed {
		c.pmu.Unlock()
		return nil, ErrClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.pmu.Unlock()
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.sendCtx(ctx, req); err != nil {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, ErrClosed
		}
		if m.Error != nil {
			return nil, fmt.Errorf("mcp %s: %s", method, m.Error.Message)
		}
		return m.Result, nil
	case <-ctx.Done():
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return nil, ctx.Err()
	}
}

// Initialize performs the handshake.
func (c *Client) Initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "syntaxis", "version": "1"},
	})
	if err != nil {
		return err
	}
	return c.sendCtx(ctx, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
}

// ListTools returns all tools (following pagination).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for page := 0; page < 20; page++ {
		var params any
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return all, nil
}

// CallTool invokes a tool and returns its text content (non-text content is summarised).
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error) {
	var a any = map[string]any{}
	if len(args) > 0 {
		a = args
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": a})
	if err != nil {
		return "", false, err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", false, err
	}
	var b strings.Builder
	for _, c := range res.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
			b.WriteString("\n")
		} else {
			fmt.Fprintf(&b, "[%s content omitted]\n", c.Type)
		}
	}
	return strings.TrimRight(b.String(), "\n"), res.IsError, nil
}

// Close closes the connection.
func (c *Client) Close() {
	_ = c.w.Close()
	select {
	case <-c.done:
	case <-time.After(time.Second):
	}
}
