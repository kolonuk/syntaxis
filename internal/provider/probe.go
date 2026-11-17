// SPDX-License-Identifier: GPL-2.0-or-later

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Verdict is the outcome of a probe. Inconclusive means the probe could not tell (the server was
// busy, unreachable or timed out), so nothing should be recorded.
type Verdict int

// Verdicts.
const (
	Inconclusive Verdict = iota
	Pass
	Fail
)

// ProbeTools checks by doing it: the model is asked to call a trivial tool. A server that refuses
// tools outright, or a model that answers in words instead of calling it, fails; the server's own
// "supports tools" claim is not trusted either way. It loads the model if it is not loaded.
func ProbeTools(ctx context.Context, p Provider, model string) (Verdict, string) {
	res, err := p.Chat(ctx, ChatRequest{Model: model, NumCtx: 2048,
		Messages: []Message{{Role: "user", Content: "Call the report_number tool with n set to 7. Do not answer in words."}},
		Tools: []ToolDef{{Name: "report_number", Description: "Reports a number.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)}}}, nil)
	if err != nil {
		if msg := strings.ToLower(err.Error()); strings.Contains(msg, "does not support tools") || strings.Contains(msg, "does not support chat") {
			return Fail, "the server says this model cannot be used with tools"
		}
		return Inconclusive, err.Error()
	}
	for _, c := range res.ToolCalls {
		if c.Name == "report_number" {
			return Pass, "called the tool"
		}
	}
	return Fail, "answered in words instead of calling the tool"
}

// A reply that is generated token by token arrives with gaps between its pieces. Held back and
// sent in blocks, most pieces arrive together, less than this apart. No model generates 30 or more
// tokens at this speed one by one.
const batchGap = time.Millisecond

// ProbeStreaming asks for a long reply and watches when the pieces arrive. A server or a proxy in
// front of it that holds the reply back and sends it at the end (or in a few blocks) shows up as
// most pieces arriving together. Fail means buffered.
func ProbeStreaming(ctx context.Context, p Provider, model string) (Verdict, string) {
	// a model may stop early; ask again a couple of times before giving up
	v, why := Inconclusive, ""
	for try := 0; try < 3 && v == Inconclusive && ctx.Err() == nil; try++ {
		v, why = probeStreamingOnce(ctx, p, model)
		if !strings.Contains(why, "too few") {
			break
		}
	}
	return v, why
}

func probeStreamingOnce(ctx context.Context, p Provider, model string) (Verdict, string) {
	var at []time.Time
	_, err := p.Chat(ctx, ChatRequest{Model: model, NumCtx: 2048,
		Messages: []Message{{Role: "user", Content: "Count from 1 to 100, one number per line, and write nothing else."}}},
		func(string) { at = append(at, time.Now()) })
	if err != nil {
		return Inconclusive, err.Error()
	}
	if len(at) < 30 {
		return Inconclusive, fmt.Sprintf("only %d pieces came back, too few to tell", len(at))
	}
	together := 0
	for i := 1; i < len(at); i++ {
		if at[i].Sub(at[i-1]) < batchGap {
			together++
		}
	}
	span := at[len(at)-1].Sub(at[0]).Round(time.Millisecond)
	if together*100 >= 80*(len(at)-1) {
		return Fail, fmt.Sprintf("%d of %d pieces arrived together (over %s): something in front of the server is buffering the reply", together, len(at), span)
	}
	return Pass, fmt.Sprintf("%d pieces over %s", len(at), span)
}
