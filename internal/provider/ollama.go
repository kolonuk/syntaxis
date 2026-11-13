// SPDX-License-Identifier: GPL-2.0-or-later

package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Ollama speaks the native Ollama API. Only read endpoints plus chat are used.
type Ollama struct{ cfg Config }

// Kind implements Provider.
func (o *Ollama) Kind() string { return KindOllama }

// Ping implements Provider.
func (o *Ollama) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	resp, err := o.cfg.do(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	return time.Since(start), nil
}

// ListModels implements Provider (GET /api/tags).
func (o *Ollama) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := o.cfg.do(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"models"`
	}
	if err := decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	var ms []ModelInfo
	for _, m := range out.Models {
		ms = append(ms, ModelInfo{Name: m.Name, Digest: m.Digest, Size: m.Size})
	}
	return ms, nil
}

// ModelDetail implements Provider (POST /api/show; read-only).
func (o *Ollama) ModelDetail(ctx context.Context, name string) (*ModelDetail, error) {
	body, _ := json.Marshal(map[string]any{"model": name})
	resp, err := o.cfg.do(ctx, http.MethodPost, "/api/show", body)
	if err != nil {
		return nil, err
	}
	var out struct {
		Details struct {
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
		ModelInfo    map[string]any `json:"model_info"`
		Parameters   string         `json:"parameters"`
		Capabilities []string       `json:"capabilities"`
	}
	if err := decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	d := &ModelDetail{Name: name, ParameterSize: out.Details.ParameterSize, Quantisation: out.Details.QuantizationLevel}
	for k, v := range out.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			if f, ok := v.(float64); ok {
				d.ContextAdvert = int(f)
			}
		}
	}
	// a Modelfile num_ctx parameter is the configured window
	for _, line := range strings.Split(out.Parameters, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "num_ctx" {
			var n int
			if _, err := fmt.Sscanf(f[1], "%d", &n); err == nil {
				d.ContextLoaded, d.ContextSource = n, "modelfile num_ctx"
			}
		}
	}
	if len(out.Capabilities) > 0 {
		has := false
		for _, c := range out.Capabilities {
			if c == "tools" {
				has = true
			}
		}
		d.SupportsTools = &has
	}
	return d, nil
}

// Loaded implements Provider (GET /api/ps).
func (o *Ollama) Loaded(ctx context.Context) ([]LoadedModel, bool, error) {
	resp, err := o.cfg.do(ctx, http.MethodGet, "/api/ps", nil)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, false, nil
	}
	var out struct {
		Models []struct {
			Name          string `json:"name"`
			Digest        string `json:"digest"`
			Size          int64  `json:"size"`
			SizeVRAM      int64  `json:"size_vram"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if err := decodeJSON(resp, &out); err != nil {
		return nil, true, err
	}
	var ms []LoadedModel
	for _, m := range out.Models {
		ms = append(ms, LoadedModel{Name: m.Name, SizeBytes: m.Size, VRAMBytes: m.SizeVRAM, ContextSize: m.ContextLength, Digest: m.Digest})
	}
	return ms, true, nil
}

type ollamaMsg struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	ToolCalls []struct {
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
}

// Chat implements Provider (POST /api/chat, NDJSON stream).
func (o *Ollama) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error) {
	var msgs []map[string]any
	for _, m := range req.Messages {
		om := map[string]any{"role": m.Role, "content": m.Content}
		if m.Role == "tool" {
			om["tool_name"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			var tcs []map[string]any
			for _, tc := range m.ToolCalls {
				tcs = append(tcs, map[string]any{"function": map[string]any{"name": tc.Name, "arguments": orEmptyObj(tc.Arguments)}})
			}
			om["tool_calls"] = tcs
		}
		msgs = append(msgs, om)
	}
	opts := map[string]any{}
	if req.NumCtx > 0 {
		opts["num_ctx"] = req.NumCtx
	}
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	payload := map[string]any{"model": req.Model, "messages": msgs, "stream": true, "options": opts}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": orEmptyObj(t.Parameters)}})
		}
		payload["tools"] = tools
	}
	body, _ := json.Marshal(payload)
	start := time.Now()
	resp, err := o.cfg.do(ctx, http.MethodPost, "/api/chat", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	res := &ChatResult{}
	var content strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var chunk struct {
			Message         ollamaMsg `json:"message"`
			Done            bool      `json:"done"`
			DoneReason      string    `json:"done_reason"`
			PromptEvalCount int       `json:"prompt_eval_count"`
			EvalCount       int       `json:"eval_count"`
			Error           string    `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			return nil, fmt.Errorf("bad stream chunk: %w", err)
		}
		if chunk.Error != "" {
			return nil, fmt.Errorf("provider error: %s", chunk.Error)
		}
		if chunk.Message.Content != "" {
			if res.TTFT == 0 {
				res.TTFT = time.Since(start)
			}
			content.WriteString(chunk.Message.Content)
			if onDelta != nil {
				onDelta(chunk.Message.Content)
			}
		}
		for i, tc := range chunk.Message.ToolCalls {
			res.ToolCalls = append(res.ToolCalls, ToolCall{ID: fmt.Sprintf("call_%d_%d", len(res.ToolCalls), i), Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
		if chunk.Done {
			res.FinishReason = chunk.DoneReason
			res.Usage = Usage{PromptTokens: chunk.PromptEvalCount, CompletionTokens: chunk.EvalCount, Reported: chunk.EvalCount > 0 || chunk.PromptEvalCount > 0}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	res.Content = content.String()
	res.Duration = time.Since(start)
	return res, nil
}

func orEmptyObj(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(`{}`)
	}
	return b
}
