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

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

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

// ErrClosed means the server went away.
var ErrClosed = errors.New("mcp: server closed the connection")

// Client talks to one MCP server over a stdio pipe, using the official MCP Go SDK
// (github.com/modelcontextprotocol/go-sdk) for the protocol itself.
type Client struct {
	r  io.Reader
	w  io.WriteCloser
	cs *sdk.ClientSession
}

// NewClient prepares a client that reads the server's output from r and writes requests to w.
func NewClient(r io.Reader, w io.WriteCloser) *Client { return &Client{r: r, w: w} }

// onlyProtocol passes through just the lines that look like JSON-RPC messages: servers may print
// log text to stdout, which the protocol does not allow but real servers do.
func onlyProtocol(r io.Reader) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), maxLine)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || line[0] != '{' {
				continue
			}
			if _, err := io.WriteString(pw, line+"\n"); err != nil {
				return
			}
		}
		_ = pw.CloseWithError(io.EOF)
	}()
	return pr
}

// Initialize connects and performs the MCP handshake.
func (c *Client) Initialize(ctx context.Context) error {
	cl := sdk.NewClient(&sdk.Implementation{Name: "syntaxis", Version: "1"}, nil)
	cs, err := cl.Connect(ctx, &sdk.IOTransport{Reader: onlyProtocol(c.r), Writer: c.w, MaxLineLength: maxLine}, nil)
	if err != nil {
		return fmt.Errorf("mcp: %w", err)
	}
	c.cs = cs
	return nil
}

func (c *Client) session() (*sdk.ClientSession, error) {
	if c.cs == nil {
		return nil, ErrClosed
	}
	return c.cs, nil
}

// ListTools returns all tools (following pagination).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	cs, err := c.session()
	if err != nil {
		return nil, err
	}
	var all []Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("mcp tools/list: %w", err)
		}
		var out Tool
		out.Name, out.Description = t.Name, t.Description
		if b, err := json.Marshal(t.InputSchema); err == nil {
			out.InputSchema = b
		}
		if t.Annotations != nil {
			out.Annotations.ReadOnlyHint = t.Annotations.ReadOnlyHint
		}
		all = append(all, out)
		if len(all) >= 500 {
			break
		}
	}
	return all, nil
}

// CallTool invokes a tool and returns its text content (other content is summarised).
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error) {
	cs, err := c.session()
	if err != nil {
		return "", false, err
	}
	var a any = map[string]any{}
	if len(args) > 0 {
		a = args
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: a})
	if err != nil {
		return "", false, fmt.Errorf("mcp tools/call: %w", err)
	}
	var b strings.Builder
	for _, content := range res.Content {
		if t, ok := content.(*sdk.TextContent); ok {
			b.WriteString(t.Text)
			b.WriteString("\n")
		} else {
			fmt.Fprintf(&b, "[%T content omitted]\n", content)
		}
	}
	return strings.TrimRight(b.String(), "\n"), res.IsError, nil
}

// Close closes the connection.
func (c *Client) Close() {
	if c.cs != nil {
		_ = c.cs.Close()
	}
	_ = c.w.Close()
}
