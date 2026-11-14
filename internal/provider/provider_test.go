// SPDX-License-Identifier: GPL-2.0-or-later

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

func mk(t *testing.T, kind string, h http.Handler, key string) Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(Config{Kind: kind, BaseURL: srv.URL + "/", APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{Kind: "ollama"}); err == nil {
		t.Fatal("missing url")
	}
	if _, err := New(Config{Kind: "nope", BaseURL: "http://x"}); err == nil {
		t.Fatal("unknown kind")
	}
	for _, k := range []string{KindOllama, KindLlamaCpp, KindLiteLLM, KindOpenAI} {
		p, err := New(Config{Kind: k, BaseURL: "http://llm.internal"})
		if err != nil || p.Kind() != k {
			t.Fatalf("%s: %v", k, err)
		}
	}
}

func ollamaMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"alpha:1b","digest":"d1","size":100},{"name":"beta:2b","digest":"d2","size":200}]}`)
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "alpha:1b") {
			http.Error(w, "no", 404)
			return
		}
		fmt.Fprint(w, `{"details":{"parameter_size":"1B","quantization_level":"Q4"},"model_info":{"llama.context_length":131072},"parameters":"num_ctx 8192\nstop x","capabilities":["completion","tools"]}`)
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"alpha:1b","digest":"d1","size":5000,"size_vram":4000,"context_length":4096}]}`)
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["stream"] != true {
			http.Error(w, "stream required", 400)
		}
		opts, _ := req["options"].(map[string]any)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Hel"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"lo"},"done":false}`)
		if opts["num_ctx"] != nil {
			fmt.Fprintln(w, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"run","arguments":{"cmd":"ls"}}}]},"done":false}`)
		}
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":12,"eval_count":3}`)
	})
	return mux
}

func TestOllamaDiscovery(t *testing.T) {
	p := mk(t, KindOllama, ollamaMux(), "")
	lat, err := p.Ping(ctx)
	if err != nil || lat <= 0 {
		t.Fatalf("ping %v %v", lat, err)
	}
	ms, err := p.ListModels(ctx)
	if err != nil || len(ms) != 2 || ms[0].Digest != "d1" {
		t.Fatalf("%+v %v", ms, err)
	}
	d, err := p.ModelDetail(ctx, "alpha:1b")
	if err != nil {
		t.Fatal(err)
	}
	if d.ContextAdvert != 131072 || d.ContextLoaded != 8192 || d.SupportsTools == nil || !*d.SupportsTools || d.ParameterSize != "1B" {
		t.Fatalf("%+v", d)
	}
	if _, err := p.ModelDetail(ctx, "missing"); err == nil {
		t.Fatal("expected error for unknown model")
	}
	l, supported, err := p.Loaded(ctx)
	if err != nil || !supported || len(l) != 1 || l[0].VRAMBytes != 4000 || l[0].ContextSize != 4096 {
		t.Fatalf("%+v %v %v", l, supported, err)
	}
}

func TestOllamaLoadedUnsupported(t *testing.T) {
	p := mk(t, KindOllama, http.NotFoundHandler(), "")
	_, supported, err := p.Loaded(ctx)
	if err != nil || supported {
		t.Fatalf("supported=%v err=%v", supported, err)
	}
}

func TestOllamaChatStreamsAndToolCalls(t *testing.T) {
	p := mk(t, KindOllama, ollamaMux(), "")
	var deltas []string
	res, err := p.Chat(ctx, ChatRequest{Model: "alpha:1b", NumCtx: 8192,
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools:    []ToolDef{{Name: "run", Description: "x", Parameters: json.RawMessage(`{"type":"object"}`)}}},
		func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "Hello" || strings.Join(deltas, "|") != "Hel|lo" {
		t.Fatalf("content %q deltas %v", res.Content, deltas)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "run" || !strings.Contains(string(res.ToolCalls[0].Arguments), "ls") {
		t.Fatalf("%+v", res.ToolCalls)
	}
	if !res.Usage.Reported || res.Usage.PromptTokens != 12 || res.Usage.CompletionTokens != 3 || res.FinishReason != "stop" {
		t.Fatalf("%+v", res)
	}
	if res.TTFT <= 0 || res.Duration < res.TTFT {
		t.Fatalf("timings %+v", res)
	}
}

func TestOllamaChatErrors(t *testing.T) {
	p := mk(t, KindOllama, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"error":"model not found"}`)
	}), "")
	if _, err := p.Chat(ctx, ChatRequest{Model: "x"}, nil); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("%v", err)
	}
	p = mk(t, KindOllama, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) }), "")
	if _, err := p.Chat(ctx, ChatRequest{Model: "x"}, nil); err == nil {
		t.Fatal("500 accepted")
	}
	p = mk(t, KindOllama, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "not json") }), "")
	if _, err := p.Chat(ctx, ChatRequest{Model: "x"}, nil); err == nil {
		t.Fatal("garbage accepted")
	}
}

func openaiMux(t *testing.T, wantKey string) http.Handler {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if wantKey != "" && r.Header.Get("Authorization") != "Bearer "+wantKey {
				http.Error(w, "unauthorized", 401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/v1/models", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"m1"},{"id":"m2"}]}`)
	}))
	mux.HandleFunc("/props", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":4096}}`)
	}))
	mux.HandleFunc("/model/info", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"model_name":"m1","model_info":{"max_input_tokens":32000,"supports_function_calling":true}}]}`)
	}))
	mux.HandleFunc("/v1/chat/completions", auth(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		send := func(s string) { fmt.Fprintf(w, "data: %s\n\n", s); fl.Flush() }
		send(`{"choices":[{"delta":{"content":"Hi "}}]}`)
		send(`{"choices":[{"delta":{"content":"there"}}]}`)
		send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"run","arguments":"{\"cmd\":"}}]}}]}`)
		send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":"tool_calls"}]}`)
		send(`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":4}}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return mux
}

func TestOpenAIDiscoveryAndAuth(t *testing.T) {
	p := mk(t, KindOpenAI, openaiMux(t, "k3y"), "k3y")
	ms, err := p.ListModels(ctx)
	if err != nil || len(ms) != 2 || ms[1].Name != "m2" {
		t.Fatalf("%+v %v", ms, err)
	}
	if _, err := p.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	bad := mk(t, KindOpenAI, openaiMux(t, "k3y"), "wrong")
	if _, err := bad.ListModels(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
	if _, supported, _ := p.Loaded(ctx); supported {
		t.Fatal("openai cannot report residency")
	}
}

func TestLlamaCppProps(t *testing.T) {
	p := mk(t, KindLlamaCpp, openaiMux(t, ""), "")
	d, _ := p.ModelDetail(ctx, "m1")
	if d.ContextLoaded != 4096 || !strings.Contains(d.ContextSource, "n_ctx") {
		t.Fatalf("%+v", d)
	}
}

func TestLiteLLMModelInfo(t *testing.T) {
	p := mk(t, KindLiteLLM, openaiMux(t, ""), "")
	d, _ := p.ModelDetail(ctx, "m1")
	if d.ContextAdvert != 32000 || d.SupportsTools == nil || !*d.SupportsTools {
		t.Fatalf("%+v", d)
	}
	d, _ = p.ModelDetail(ctx, "unknown")
	if d.ContextAdvert != 0 || d.SupportsTools != nil {
		t.Fatalf("unknown model got details: %+v", d)
	}
}

func TestOpenAIChatAssemblesToolCallFragments(t *testing.T) {
	p := mk(t, KindOpenAI, openaiMux(t, ""), "")
	var got strings.Builder
	res, err := p.Chat(ctx, ChatRequest{Model: "m1", Messages: []Message{{Role: "user", Content: "x"}}}, func(s string) { got.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "Hi there" || got.String() != "Hi there" {
		t.Fatalf("%q", res.Content)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "c1" || string(res.ToolCalls[0].Arguments) != `{"cmd":"ls"}` {
		t.Fatalf("%+v", res.ToolCalls)
	}
	var args map[string]string
	if err := json.Unmarshal(res.ToolCalls[0].Arguments, &args); err != nil || args["cmd"] != "ls" {
		t.Fatal("arguments not valid JSON")
	}
	if !res.Usage.Reported || res.Usage.PromptTokens != 7 || res.FinishReason != "tool_calls" {
		t.Fatalf("%+v", res)
	}
}

func TestOpenAIChatMidStreamError(t *testing.T) {
	p := mk(t, KindOpenAI, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"context length exceeded\"}}\n\n")
	}), "")
	if _, err := p.Chat(ctx, ChatRequest{Model: "m"}, nil); err == nil || !strings.Contains(err.Error(), "context length") {
		t.Fatalf("%v", err)
	}
}

func TestUnreachableAndCancel(t *testing.T) {
	p, _ := New(Config{Kind: KindOllama, BaseURL: "http://127.0.0.1:1"})
	if _, err := p.ListModels(ctx); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("%v", err)
	}
	slow := mk(t, KindOpenAI, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(1 * time.Second):
		}
	}), "")
	c, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := slow.Chat(c, ChatRequest{Model: "m"}, nil); err == nil {
		t.Fatal("expected cancel error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancel not honoured")
	}
}

func TestChatReportsPhases(t *testing.T) {
	p := mk(t, KindOllama, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"","thinking":"hmm"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Hi"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"content":""},"done":true,"done_reason":"stop","eval_count":2}`)
	}), "")
	var phases []string
	if _, err := p.Chat(ctx, ChatRequest{Model: "x", Phase: func(s string) { phases = append(phases, s) }}, nil); err != nil {
		t.Fatal(err)
	}
	if len(phases) < 2 || phases[0] != PhaseFirstByte || phases[1] != PhaseThinking {
		t.Fatalf("phases %v", phases)
	}
	o := mk(t, KindOpenAI, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hmm\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: [DONE]\n\n")
	}), "")
	phases = nil
	if _, err := o.Chat(ctx, ChatRequest{Model: "x", Phase: func(s string) { phases = append(phases, s) }}, nil); err != nil {
		t.Fatal(err)
	}
	if len(phases) < 2 || phases[0] != PhaseFirstByte || phases[1] != PhaseThinking {
		t.Fatalf("openai phases %v", phases)
	}
}
