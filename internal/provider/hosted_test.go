// SPDX-License-Identifier: GPL-2.0-or-later

package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A hosted OpenAI-compatible service is an ordinary HTTP server, so these tests run a stand-in on
// this machine that behaves like one: bearer key, model list, streamed replies with tool calls and
// usage, rate limiting.
func hostedServer(t *testing.T, key string, limited *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if limited != nil && limited.Add(-1) >= 0 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"alpha"}]}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			for _, c := range []string{
				`{"choices":[{"delta":{"content":"Hel"}}]}`,
				`{"choices":[{"delta":{"content":"lo"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"run","arguments":"{\"cmd\":"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5}}`,
			} {
				_, _ = fmt.Fprintf(w, ": keep-alive\n\ndata: %s\n\n", c)
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHostedServiceStreamsTextToolCallsAndUsage(t *testing.T) {
	srv := hostedServer(t, "the-key", nil)
	p, err := New(Config{Kind: KindOpenAI, BaseURL: srv.URL, APIKey: "the-key"})
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.ListModels(ctx)
	if err != nil || len(ms) != 1 || ms[0].Name != "alpha" {
		t.Fatalf("models: %v %v", ms, err)
	}
	// what a local server reports and a hosted one does not: nothing here may fail for lack of it
	if d, err := p.ModelDetail(ctx, "alpha"); err != nil || d.SupportsTools != nil || d.ContextLoaded != 0 {
		t.Fatalf("detail: %+v %v", d, err)
	}
	if _, supported, err := p.Loaded(ctx); err != nil || supported {
		t.Fatalf("loaded: %v %v", supported, err)
	}
	var got strings.Builder
	res, err := p.Chat(ctx, ChatRequest{Model: "alpha", Messages: []Message{{Role: "user", Content: "hi"}},
		Tools: []ToolDef{{Name: "run", Description: "runs", Parameters: []byte(`{"type":"object"}`)}}}, func(s string) { got.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "Hello" || res.Content != "Hello" || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "run" || string(res.ToolCalls[0].Arguments) != `{"cmd":"ls"}` {
		t.Fatalf("reply: %q %+v", got.String(), res)
	}
	if !res.Usage.Reported || res.Usage.PromptTokens != 12 || res.Usage.CompletionTokens != 5 || res.FinishReason != "tool_calls" {
		t.Fatalf("usage: %+v %q", res.Usage, res.FinishReason)
	}
}

func TestHostedServiceRateLimitIsRetriedAndABadKeyIsNot(t *testing.T) {
	var limited atomic.Int32
	limited.Store(2)
	srv := hostedServer(t, "the-key", &limited)
	// without retries the rate limit is an error
	p, _ := New(Config{Kind: KindOpenAI, BaseURL: srv.URL, APIKey: "the-key"})
	limited.Store(1)
	if _, err := p.ListModels(ctx); err == nil {
		t.Fatal("a 429 was not reported")
	}
	// with them it is asked again after the pause the service names
	limited.Store(2)
	p, _ = New(Config{Kind: KindOpenAI, BaseURL: srv.URL, APIKey: "the-key", Retries: 2})
	if ms, err := p.ListModels(ctx); err != nil || len(ms) != 1 {
		t.Fatalf("retried: %v %v", ms, err)
	}
	// a wrong key is never retried, and the error does not say the key
	var asked atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer bad.Close()
	p, _ = New(Config{Kind: KindOpenAI, BaseURL: bad.URL, APIKey: "very-secret", Retries: 3})
	_, err := p.ListModels(ctx)
	if !errors.Is(err, ErrAuth) || strings.Contains(err.Error(), "very-secret") || asked.Load() != 1 {
		t.Fatalf("bad key: %v after %d requests", err, asked.Load())
	}
}

func TestHostedServiceRetryStopsWhenTheCallIsCancelled(t *testing.T) {
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "20")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer always.Close()
	p, _ := New(Config{Kind: KindOpenAI, BaseURL: always.URL, APIKey: "k", Retries: 3})
	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := p.ListModels(cctx); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("a cancelled call kept waiting: %v after %v", err, time.Since(start))
	}
}
