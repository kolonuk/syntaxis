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

// OpenAI speaks the OpenAI-compatible API (llama.cpp server, LiteLLM, others).
type OpenAI struct{ cfg Config }

// Kind implements Provider.
func (o *OpenAI) Kind() string { return o.cfg.Kind }

// Ping implements Provider.
func (o *OpenAI) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	resp, err := o.cfg.do(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	return time.Since(start), nil
}

// ListModels implements Provider (GET /v1/models).
func (o *OpenAI) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := o.cfg.do(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	var ms []ModelInfo
	for _, m := range out.Data {
		ms = append(ms, ModelInfo{Name: m.ID})
	}
	return ms, nil
}

// ModelDetail implements Provider. llama.cpp exposes the real window at /props;
// LiteLLM exposes limits at /model/info. Everything is best-effort.
func (o *OpenAI) ModelDetail(ctx context.Context, name string) (*ModelDetail, error) {
	d := &ModelDetail{Name: name}
	switch o.cfg.Kind {
	case KindLlamaCpp:
		if resp, err := o.cfg.do(ctx, http.MethodGet, "/props", nil); err == nil && resp.StatusCode/100 == 2 {
			var p struct {
				Default struct {
					NCtx int `json:"n_ctx"`
				} `json:"default_generation_settings"`
				NCtx int `json:"n_ctx"`
			}
			if json.NewDecoder(resp.Body).Decode(&p) == nil {
				n := p.Default.NCtx
				if n == 0 {
					n = p.NCtx
				}
				if n > 0 {
					d.ContextLoaded, d.ContextSource = n, "llama.cpp /props n_ctx"
				}
			}
			_ = resp.Body.Close()
		} else if resp != nil {
			_ = resp.Body.Close()
		}
	case KindLiteLLM:
		if resp, err := o.cfg.do(ctx, http.MethodGet, "/model/info", nil); err == nil && resp.StatusCode/100 == 2 {
			var p struct {
				Data []struct {
					ModelName string `json:"model_name"`
					Info      struct {
						MaxInput      int   `json:"max_input_tokens"`
						MaxTokens     int   `json:"max_tokens"`
						SupportsFuncs *bool `json:"supports_function_calling"`
					} `json:"model_info"`
				} `json:"data"`
			}
			if json.NewDecoder(resp.Body).Decode(&p) == nil {
				for _, m := range p.Data {
					if m.ModelName == name {
						d.ContextAdvert = m.Info.MaxInput
						if d.ContextAdvert == 0 {
							d.ContextAdvert = m.Info.MaxTokens
						}
						d.SupportsTools = m.Info.SupportsFuncs
					}
				}
			}
			_ = resp.Body.Close()
		} else if resp != nil {
			_ = resp.Body.Close()
		}
	}
	return d, nil
}

// Loaded implements Provider: OpenAI-compatible servers cannot say what is resident.
func (o *OpenAI) Loaded(context.Context) ([]LoadedModel, bool, error) { return nil, false, nil }

// Chat implements Provider (POST /v1/chat/completions, SSE).
func (o *OpenAI) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error) {
	var msgs []map[string]any
	for _, m := range req.Messages {
		om := map[string]any{"role": m.Role, "content": m.Content}
		if m.Role == "tool" {
			om["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			var tcs []map[string]any
			for _, tc := range m.ToolCalls {
				tcs = append(tcs, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": string(orEmptyObj(tc.Arguments))}})
			}
			om["tool_calls"] = tcs
		}
		msgs = append(msgs, om)
	}
	payload := map[string]any{"model": req.Model, "messages": msgs, "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": orEmptyObj(t.Parameters)}})
		}
		payload["tools"] = tools
	}
	body, _ := json.Marshal(payload)
	start := time.Now()
	resp, err := o.cfg.do(ctx, http.MethodPost, "/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	res := &ChatResult{}
	var content strings.Builder
	type partial struct {
		id, name string
		args     strings.Builder
	}
	partials := map[int]*partial{}
	firstSeen := false
	var order []int
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
					Reason2   string `json:"reasoning"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("bad stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("provider error: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			res.Usage = Usage{PromptTokens: chunk.Usage.Prompt, CompletionTokens: chunk.Usage.Completion, Reported: true}
		}
		if req.Phase != nil {
			if !firstSeen {
				firstSeen = true
				req.Phase(PhaseFirstByte)
			}
			for _, c := range chunk.Choices {
				if (c.Delta.Reasoning != "" || c.Delta.Reason2 != "") && c.Delta.Content == "" {
					req.Phase(PhaseThinking)
				}
			}
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				if res.TTFT == 0 {
					res.TTFT = time.Since(start)
				}
				content.WriteString(c.Delta.Content)
				if onDelta != nil {
					onDelta(c.Delta.Content)
				}
			}
			for _, tc := range c.Delta.ToolCalls {
				p, ok := partials[tc.Index]
				if !ok {
					p = &partial{}
					partials[tc.Index] = p
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					p.id = tc.ID
				}
				if tc.Function.Name != "" {
					p.name = tc.Function.Name
				}
				p.args.WriteString(tc.Function.Arguments)
			}
			if c.FinishReason != "" {
				res.FinishReason = c.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for _, idx := range order {
		p := partials[idx]
		id := p.id
		if id == "" {
			id = fmt.Sprintf("call_%d", idx)
		}
		res.ToolCalls = append(res.ToolCalls, ToolCall{ID: id, Name: p.name, Arguments: orEmptyObj(json.RawMessage(p.args.String()))})
	}
	res.Content = content.String()
	res.Duration = time.Since(start)
	return res, nil
}
