// SPDX-License-Identifier: GPL-2.0-or-later

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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

// The probes against the real server and real models. Ollama streams as it generates, so the
// streaming probe must pass; a buffering proxy cannot be put in front of it here.
func TestLiveProbes(t *testing.T) {
	p, env := live(t, KindOllama, "")
	c, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	if v, why := ProbeStreaming(c, p, env["MODEL_TINY"]); v == Fail {
		t.Fatalf("a direct Ollama stream was judged buffered: %s", why)
	} else {
		t.Logf("streaming: %d %s", v, why)
	}
	// the models on the server change; use the small one if it is there, else the main one
	have := map[string]bool{}
	listed, _ := p.ListModels(ctx)
	for _, m := range listed {
		have[m.Name] = true
	}
	toolModel := env["MODEL_SMALL"]
	if !have[toolModel] {
		toolModel = env["MODEL_MAIN"]
	}
	v, why := ProbeTools(c, p, toolModel)
	t.Logf("tools: %d %s", v, why)
	if v != Pass {
		t.Fatalf("a tool-capable model should call a trivial tool: %s", why)
	}
	// a model the server says has no tools is refused by the server and judged Fail
	ms, _ := p.ListModels(ctx)
	for _, m := range ms {
		d, err := p.ModelDetail(ctx, m.Name)
		if err == nil && d.SupportsTools != nil && !*d.SupportsTools {
			if v, why := ProbeTools(c, p, m.Name); v != Fail {
				t.Fatalf("%s reports no tools but the probe said %d (%s)", m.Name, v, why)
			}
			return
		}
	}
	t.Log("NOT TESTED: a model without tool support (every model on this server has it)")
}

func TestProbeIsInconclusiveWhenTheServerIsUnreachable(t *testing.T) {
	p, _ := New(Config{Kind: KindOllama, BaseURL: "http://127.0.0.1:1"})
	if v, _ := ProbeTools(ctx, p, "m"); v != Inconclusive {
		t.Fatalf("%d", v)
	}
	if v, _ := ProbeStreaming(ctx, p, "m"); v != Inconclusive {
		t.Fatalf("%d", v)
	}
}

// A real nginx that holds the stream back, in front of the real Ollama: the probe must catch it.
// The same nginx with buffering off must pass.
func TestLiveProbeCatchesARealBufferingProxy(t *testing.T) {
	_, env := live(t, KindOllama, "")
	testenv.Docker(t)
	run := func(buffering, gzip string) string {
		dir := t.TempDir()
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		conf := fmt.Sprintf(`events {}
http { server { listen 127.0.0.1:%d; %s location / { proxy_pass %s; proxy_http_version 1.1; proxy_buffering %s; proxy_buffer_size 128k; proxy_buffers 16 256k; proxy_read_timeout 300s; } } }
`, port, gzip, strings.TrimRight(env["OLLAMA_URL"], "/"), buffering)
		if err := os.WriteFile(filepath.Join(dir, "nginx.conf"), []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("syntaxis-test-nginx-%d", time.Now().UnixNano()%1e9)
		if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "--network", "host", // so it reaches the model server the way the host does
			"-v", filepath.Join(dir, "nginx.conf")+":/etc/nginx/nginx.conf:ro", "nginx:alpine").CombinedOutput(); err != nil {
			testenv.NotTested(t, "the nginx image could not be started: "+strings.TrimSpace(string(out)))
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
		time.Sleep(2 * time.Second)
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	// the common mistake: compression turned on for the route, which holds the stream back in blocks
	cases := []struct {
		name, buffering, gzip string
		want                  Verdict
	}{
		{"buffering and gzip", "on", "gzip on; gzip_proxied any; gzip_types application/x-ndjson; gzip_min_length 1;", Fail},
		{"streaming through", "off", "", Pass},
	}
	for _, tc := range cases {
		p, _ := New(Config{Kind: KindOllama, BaseURL: run(tc.buffering, tc.gzip)})
		if v, why := ProbeStreaming(c, p, env["MODEL_TINY"]); v != tc.want {
			t.Fatalf("%s: verdict %d (%s), want %d", tc.name, v, why, tc.want)
		}
	}
}
