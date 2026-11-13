// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ParsedCall is a tool call recovered from model output.
type ParsedCall struct {
	Name string
	Args json.RawMessage
}

var (
	fenceRe   = regexp.MustCompile("(?s)```(?:json|tool_call|tool|javascript|js)?\\s*\\n?(.*?)```")
	tagRe     = regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)
	funcTagRe = regexp.MustCompile(`(?s)<function=([a-zA-Z0-9_\-]+)>\s*(.*?)\s*</function>`)
)

// ParseToolCallsFromText recovers tool calls from assistant text for models
// that emit them as JSON in prose rather than via the structured API. Only
// names in known are accepted. It returns the calls and the text with the
// call blocks removed.
func ParseToolCallsFromText(text string, known map[string]bool) ([]ParsedCall, string) {
	var calls []ParsedCall
	rest := text
	take := func(re *regexp.Regexp, group int) {
		rest = re.ReplaceAllStringFunc(rest, func(m string) string {
			g := re.FindStringSubmatch(m)
			if g == nil {
				return m
			}
			got := extractCalls(g[group], known)
			if len(got) == 0 {
				return m // not a tool call; leave the text (e.g. a normal code block)
			}
			calls = append(calls, got...)
			return ""
		})
	}
	take(tagRe, 1)
	// <function=name>{...}</function>
	rest = funcTagRe.ReplaceAllStringFunc(rest, func(m string) string {
		g := funcTagRe.FindStringSubmatch(m)
		if g == nil || !known[g[1]] {
			return m
		}
		args := strings.TrimSpace(g[2])
		if !json.Valid([]byte(args)) {
			return m
		}
		calls = append(calls, ParsedCall{Name: g[1], Args: json.RawMessage(args)})
		return ""
	})
	take(fenceRe, 1)
	if len(calls) == 0 {
		// bare JSON anywhere in the text
		if got := extractCalls(strings.TrimSpace(rest), known); len(got) > 0 {
			return got, ""
		}
		if got, remaining := scanBareJSON(rest, known); len(got) > 0 {
			return got, remaining
		}
	}
	return calls, strings.TrimSpace(rest)
}

// scanBareJSON finds the first balanced JSON object/array in text that parses as a tool call.
func scanBareJSON(text string, known map[string]bool) ([]ParsedCall, string) {
	for i := 0; i < len(text); i++ {
		if text[i] != '{' && text[i] != '[' {
			continue
		}
		end := matchBracket(text, i)
		if end < 0 {
			continue
		}
		if got := extractCalls(text[i:end+1], known); len(got) > 0 {
			return got, strings.TrimSpace(text[:i] + text[end+1:])
		}
		i = end
	}
	return nil, text
}

func matchBracket(s string, start int) int {
	open := s[start]
	closeCh := byte('}')
	if open == '[' {
		closeCh = ']'
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func extractCalls(s string, known map[string]bool) []ParsedCall {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var raw json.RawMessage = json.RawMessage(s)
	if !json.Valid(raw) {
		return nil
	}
	if s[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil {
			return nil
		}
		var out []ParsedCall
		for _, a := range arr {
			if c, ok := oneCall(a, known); ok {
				out = append(out, c)
			}
		}
		return out
	}
	if c, ok := oneCall(raw, known); ok {
		return []ParsedCall{c}
	}
	return nil
}

func oneCall(raw json.RawMessage, known map[string]bool) (ParsedCall, bool) {
	var o struct {
		Name       string          `json:"name"`
		Tool       string          `json:"tool"`
		ToolName   string          `json:"tool_name"`
		Function   json.RawMessage `json:"function"`
		Arguments  json.RawMessage `json:"arguments"`
		Args       json.RawMessage `json:"args"`
		Parameters json.RawMessage `json:"parameters"`
		Input      json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ParsedCall{}, false
	}
	// {"function":{"name":..., "arguments":...}} (OpenAI-ish)
	if len(o.Function) > 0 && o.Function[0] == '{' {
		return oneCall(o.Function, known)
	}
	name := firstNonEmpty(o.Name, o.Tool, o.ToolName)
	if name == "" && len(o.Function) > 0 {
		var fn string
		if json.Unmarshal(o.Function, &fn) == nil {
			name = fn
		}
	}
	if !known[name] {
		return ParsedCall{}, false
	}
	args := firstRaw(o.Arguments, o.Args, o.Parameters, o.Input)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	// double-encoded arguments: "{\"cmd\":\"ls\"}"
	if args[0] == '"' {
		var inner string
		if json.Unmarshal(args, &inner) == nil && json.Valid([]byte(inner)) {
			args = json.RawMessage(inner)
		} else {
			return ParsedCall{}, false
		}
	}
	if args[0] != '{' {
		return ParsedCall{}, false
	}
	return ParsedCall{Name: name, Args: args}, true
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func firstRaw(rs ...json.RawMessage) json.RawMessage {
	for _, r := range rs {
		if len(r) > 0 && string(r) != "null" {
			return r
		}
	}
	return nil
}
