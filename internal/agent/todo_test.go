// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/provider"
)

func TestTodoWriteValidates(t *testing.T) {
	x, b := newExec()
	ok := run(x, ToolTodo, `{"todos":[{"content":"read the code","status":"completed"},{"content":"fix it","status":"in_progress"},{"content":"test","status":"pending"},{"content":"no status"}]}`)
	if ok.IsError || !ok.TodosSet || len(ok.Todos) != 4 || ok.Todos[3].Status != "pending" || !strings.Contains(ok.Text, "1 of 4 done") {
		t.Fatalf("%+v", ok)
	}
	for name, args := range map[string]string{
		"two in progress": `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`,
		"bad status":      `{"todos":[{"content":"a","status":"doing"}]}`,
		"empty item":      `{"todos":[{"content":"  ","status":"pending"}]}`,
		"not a list":      `{"todos":"fix it"}`,
		"missing":         `{}`,
	} {
		o := run(x, ToolTodo, args)
		if name == "missing" {
			// an empty list clears the task list; that is allowed
			if o.IsError || len(o.Todos) != 0 {
				t.Errorf("%s: %+v", name, o)
			}
			continue
		}
		if !o.IsError || o.TodosSet {
			t.Errorf("%s accepted: %+v", name, o)
		}
	}
	long := run(x, ToolTodo, `{"todos":[{"content":"`+strings.Repeat("x", 500)+`","status":"pending"}]}`)
	if long.IsError || len(long.Todos[0].Content) != maxTodoLength {
		t.Fatalf("long items are cut: %+v", long)
	}
	many := `{"todos":[` + strings.Repeat(`{"content":"a","status":"pending"},`, maxTodos) + `{"content":"a","status":"pending"}]}`
	if o := run(x, ToolTodo, many); !o.IsError {
		t.Fatal("an enormous list was accepted")
	}
	if len(b.cmds) != 0 {
		t.Fatalf("the task list must not run anything in the sandbox: %v", b.cmds)
	}
}

func TestTodoWriteReachesTheUIAndNeedsNoApproval(t *testing.T) {
	h := newHarness(t)
	h.a.Cfg.Autonomy = AskAll
	h.model.replies = []*provider.ChatResult{
		toolReply(call("t1", ToolTodo, `{"todos":[{"content":"step one","status":"in_progress"}]}`)),
		{Content: "done"},
	}
	r := h.a.Run(context.Background(), "do it")
	if r.Status != StatusFinished {
		t.Fatalf("%+v", r)
	}
	if len(h.approvals) != 0 {
		t.Fatal("updating the task list asked for approval")
	}
	var got []Todo
	for _, e := range h.events {
		if e.Kind == EvTodos {
			got, _ = e.Data["todos"].([]Todo)
		}
	}
	if len(got) != 1 || got[0].Content != "step one" || got[0].Status != "in_progress" {
		t.Fatalf("no todos event: %+v", h.events)
	}
}
