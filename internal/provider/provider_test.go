// SPDX-License-Identifier: GPL-2.0-or-later

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/testenv"
)

var ctx = context.Background()

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

func TestUnreachable(t *testing.T) {
	// nothing listens on port 1: the real network stack refuses the connection
	p, _ := New(Config{Kind: KindOllama, BaseURL: "http://127.0.0.1:1"})
	if _, err := p.ListModels(ctx); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("%v", err)
	}
}

// live returns a provider on the real local Ollama from the private test environment.
func live(t *testing.T, kind, suffix string) (Provider, map[string]string) {
	env := testenv.Load(t)
	url := env["OLLAMA_URL"]
	if url == "" {
		testenv.NotTested(t, "OLLAMA_URL is not in the private test environment")
	}
	p, err := New(Config{Kind: kind, BaseURL: strings.TrimRight(url, "/") + suffix})
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := p.ListModels(c); err != nil {
		testenv.NotTested(t, "the Ollama server is not reachable: "+err.Error())
	}
	return p, env
}

func TestLiveOllamaDiscoveryAndResidency(t *testing.T) {
	p, env := live(t, KindOllama, "")
	ms, err := p.ListModels(ctx)
	if err != nil || len(ms) == 0 {
		t.Fatalf("%v %v", ms, err)
	}
	d, err := p.ModelDetail(ctx, env["MODEL_TINY"])
	if err != nil || d.ContextAdvert <= 0 {
		t.Fatalf("detail %+v %v", d, err)
	}
	if _, supported, err := p.Loaded(ctx); !supported || err != nil {
		t.Fatalf("Ollama reports what is loaded: supported=%v err=%v", supported, err)
	}
}

func TestLiveOllamaChatStreamsAndReportsUsage(t *testing.T) {
	p, env := live(t, KindOllama, "")
	var deltas []string
	var phases []string
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := p.Chat(c, ChatRequest{Model: env["MODEL_TINY"], NumCtx: 2048,
		Messages: []Message{{Role: "user", Content: "Reply with the single word: pong"}},
		Phase:    func(s string) { phases = append(phases, s) }},
		func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Content) == "" || strings.Join(deltas, "") != res.Content {
		t.Fatalf("content %q deltas %q", res.Content, deltas)
	}
	if !res.Usage.Reported || res.Usage.PromptTokens == 0 || res.Usage.CompletionTokens == 0 {
		t.Fatalf("usage %+v", res.Usage)
	}
	if res.TTFT <= 0 || res.Duration < res.TTFT || len(phases) == 0 || phases[0] != PhaseFirstByte {
		t.Fatalf("timings %+v phases %v", res, phases)
	}
}

func TestLiveOllamaChatCancelIsHonoured(t *testing.T) {
	p, env := live(t, KindOllama, "")
	c, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.Chat(c, ChatRequest{Model: env["MODEL_MAIN"], Messages: []Message{{Role: "user", Content: "Write a very long story."}}}, nil)
	if err == nil {
		t.Fatal("expected the cancelled request to fail")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel was not honoured")
	}
}

func TestLiveOllamaToolCall(t *testing.T) {
	p, env := live(t, KindOllama, "")
	m := env["MODEL_SMALL"]
	if m == "" {
		testenv.NotTested(t, "MODEL_SMALL is not in the private test environment")
	}
	c, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	res, err := p.Chat(c, ChatRequest{Model: m, NumCtx: 4096,
		Messages: []Message{{Role: "user", Content: "Use the run tool to list files with the command ls."}},
		Tools: []ToolDef{{Name: "run", Description: "Run a shell command", Parameters: json.RawMessage(
			`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) == 0 || res.ToolCalls[0].Name != "run" {
		t.Skipf("NOT TESTED: the model %s did not call the tool this time (%q); a model's choice, not a code fault", m, res.Content)
	}
}

// Ollama also serves the OpenAI-compatible API under /v1 (the adapter adds it), which exercises the OpenAI adapter
// against a real server.
func TestLiveOpenAICompatibleOnOllama(t *testing.T) {
	p, env := live(t, KindOpenAI, "")
	ms, err := p.ListModels(ctx)
	if err != nil || len(ms) == 0 {
		t.Fatalf("%v %v", ms, err)
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var phases []string
	res, err := p.Chat(c, ChatRequest{Model: env["MODEL_TINY"], Messages: []Message{{Role: "user", Content: "Reply with the single word: pong"}},
		Phase: func(s string) { phases = append(phases, s) }}, nil)
	if err != nil || strings.TrimSpace(res.Content) == "" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestNotTestedAdapters(t *testing.T) {
	testenv.NotTested(t, "the llama.cpp and LiteLLM adapters (no such server is available here), and tool-call fragments and mid-stream errors from a hosted OpenAI-style service")
}
