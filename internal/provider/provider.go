// SPDX-License-Identifier: GPL-2.0-or-later

// Package provider adapts LLM servers (Ollama, llama.cpp, LiteLLM, generic
// OpenAI-compatible) behind one read-only interface. Syntaxis never pulls,
// loads or deletes models on the server.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Kinds of provider.
const (
	KindOllama   = "ollama"
	KindLlamaCpp = "llamacpp"
	KindLiteLLM  = "litellm"
	KindOpenAI   = "openai"
)

// ErrUnreachable wraps transport failures with a user-presentable message.
var ErrUnreachable = errors.New("provider unreachable")

// ErrAuth is returned for 401/403 responses.
var ErrAuth = errors.New("provider rejected credentials")

// ModelInfo is a discovered model.
type ModelInfo struct {
	Name   string
	Digest string
	Size   int64
}

// ModelDetail holds what the server will tell us about a model. Unknown
// numeric values are 0; SupportsTools is nil when unknown.
type ModelDetail struct {
	Name          string
	ContextAdvert int    // model's advertised maximum
	ContextLoaded int    // effective window the server actually runs with (0 unknown)
	ContextSource string // where ContextLoaded came from
	SupportsTools *bool
	ParameterSize string
	Quantisation  string
}

// LoadedModel is a model currently resident on the server (Ollama /api/ps).
type LoadedModel struct {
	Name        string
	SizeBytes   int64
	VRAMBytes   int64
	ContextSize int
	Digest      string
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Message is a chat message.
type Message struct {
	Role       string     `json:"role"` // system|user|assistant|tool
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ChatRequest is a model call.
type ChatRequest struct {
	Model       string
	Messages    []Message
	Tools       []ToolDef
	NumCtx      int // requested window (Ollama num_ctx); 0 = server default
	Temperature *float64
	MaxTokens   int
}

// Usage is token accounting as reported by the provider.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	Reported         bool
}

// ChatResult is the final aggregated response.
type ChatResult struct {
	Content      string
	ToolCalls    []ToolCall
	Usage        Usage
	FinishReason string
	TTFT         time.Duration // time to first token
	Duration     time.Duration
}

// Provider is implemented by each adapter.
type Provider interface {
	Kind() string
	// Ping checks reachability and measures latency.
	Ping(ctx context.Context) (time.Duration, error)
	ListModels(ctx context.Context) ([]ModelInfo, error)
	ModelDetail(ctx context.Context, name string) (*ModelDetail, error)
	// Loaded reports resident models; supported=false when the server cannot say.
	Loaded(ctx context.Context) (models []LoadedModel, supported bool, err error)
	// Chat streams; onDelta is called with text deltas (may be nil).
	Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error)
}

// Config configures a provider adapter.
type Config struct {
	Kind    string
	BaseURL string
	APIKey  string
	Client  *http.Client // optional
}

// New builds the adapter for cfg.Kind.
func New(cfg Config) (Provider, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("provider: base URL required")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Client == nil {
		cfg.Client = &http.Client{} // per-request deadlines come from ctx
	}
	switch cfg.Kind {
	case KindOllama:
		return &Ollama{cfg: cfg}, nil
	case KindLlamaCpp, KindLiteLLM, KindOpenAI:
		return &OpenAI{cfg: cfg}, nil
	}
	return nil, fmt.Errorf("provider: unknown kind %q", cfg.Kind)
}

func (c Config) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rdr *strings.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	var req *http.Request
	var err error
	if rdr != nil {
		req, err = http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, c.BaseURL+path, nil)
	}
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, ErrAuth
	}
	return resp, nil
}

func decodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
