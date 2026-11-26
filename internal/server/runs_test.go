// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/agent"
	"github.com/kolonuk/syntaxis/internal/provider"
	"github.com/kolonuk/syntaxis/internal/store"
)

func newTestRun() *Run {
	r, _ := NewRuns().Start("s1", func(ctx context.Context, r *Run) { <-ctx.Done() })
	return r
}

func TestSubscribeReplaysOnlyWhatTheBrowserMissed(t *testing.T) {
	r := newTestRun()
	defer r.Stop()
	for i := 1; i <= 5; i++ {
		r.Emit(agent.Event{Kind: "phase", Data: map[string]any{"n": i}})
	}
	replay, _, unsub := r.Subscribe(3)
	defer unsub()
	if len(replay) != 2 || replay[0].Seq != 4 || replay[1].Seq != 5 {
		t.Fatalf("want events 4 and 5, got %+v", replay)
	}
	all, _, unsub2 := r.Subscribe(0)
	defer unsub2()
	if len(all) != 5 {
		t.Fatalf("want all 5, got %d", len(all))
	}
}

func TestSubscribeNumbersSurviveTrimming(t *testing.T) {
	r := newTestRun()
	defer r.Stop()
	for i := 1; i <= replayCap+10; i++ {
		r.Emit(agent.Event{Kind: "phase"})
	}
	replay, _, unsub := r.Subscribe(replayCap + 8)
	defer unsub()
	if len(replay) != 2 || replay[0].Seq != replayCap+9 {
		t.Fatalf("numbers must count every event, not the trimmed list: %+v", replay)
	}
}

func TestSlowSubscriberIsDisconnectedNotLeftToMissEvents(t *testing.T) {
	r := newTestRun()
	defer r.Stop()
	_, ch, unsub := r.Subscribe(0)
	defer unsub()
	for i := 0; i < 300; i++ { // more than the 256 the channel holds, with nobody reading
		r.Emit(agent.Event{Kind: "assistant_delta"})
	}
	n := 0
	for range ch { // drains what fits, then sees the close
		n++
	}
	if n == 0 || n > 256 {
		t.Fatalf("read %d events before the close", n)
	}
	// the record is complete, so a reconnect with the last number seen gets the rest
	replay, _, unsub2 := r.Subscribe(n)
	defer unsub2()
	if len(replay) != 300-n {
		t.Fatalf("reconnect after %d should replay %d, got %d", n, 300-n, len(replay))
	}
}

func TestEventsEndpointResumesAfterLastEventID(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	release := make(chan struct{})
	e.app.RunAgent = func(ctx context.Context, run *Run, s *store.ChatSession, u *store.User, msg string) {
		for i := 1; i <= 3; i++ {
			run.Emit(agent.Event{Kind: agent.EvProgress, Data: map[string]any{"message": fmt.Sprintf("step %d", i)}})
		}
		<-release
	}
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	if code, _, _ := u.do("POST", "/api/sessions/"+id+"/messages", map[string]string{"text": "go"}); code != 202 {
		t.Fatalf("start: %d", code)
	}
	read := func(lastID string, until string) string {
		req, _ := http.NewRequest("GET", e.user.URL+"/api/sessions/"+id+"/events", nil)
		for _, ck := range u.hc.Jar.Cookies(mustURL(e.user.URL)) {
			req.AddCookie(ck)
		}
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := http.DefaultClient.Do(req.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var sb strings.Builder
		buf := make([]byte, 4096)
		for !strings.Contains(sb.String(), until) {
			n, err := resp.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return sb.String()
	}
	first := read("", "step 3")
	var ids []string
	for _, l := range strings.Split(first, "\n") {
		if strings.HasPrefix(l, "id: ") {
			ids = append(ids, strings.TrimPrefix(l, "id: "))
		}
	}
	if len(ids) < 3 {
		t.Fatalf("every event carries an id: %q", first)
	}
	// reconnect after the second: only the third comes again
	again := read(ids[1], "step 3")
	if strings.Contains(again, "step 1") || strings.Contains(again, "step 2") || !strings.Contains(again, "step 3") {
		t.Fatalf("resume after %s: %q", ids[1], again)
	}
	// an id from another run counts for nothing: everything is sent
	other := read("1-3", "step 3")
	if !strings.Contains(other, "step 1") {
		t.Fatalf("an id from another run must not skip events: %q", other)
	}
	close(release)
}

func TestContextEstimateIsSentBeforeTheSandboxExists(t *testing.T) {
	e := newEnv(t)
	seedModel(e)
	u := e.userSession("a@example.com")
	_, s, _ := u.do("POST", "/api/sessions", map[string]string{"model": "prov/m1"})
	id := s["id"].(string)
	bg := context.Background()
	e.app.persistMessage(bg, id, provider.Message{Role: "user", Content: strings.Repeat("word ", 400)}, 0, 0)
	cs, err := e.app.Store.GetChatSession(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRun()
	defer r.Stop()
	e.app.emitContextEstimate(bg, r, cs, 8192)
	replay, _, unsub := r.Subscribe(0)
	defer unsub()
	if len(replay) != 1 || replay[0].Event.Kind != agent.EvContext {
		t.Fatalf("want one context event, got %+v", replay)
	}
	d := replay[0].Event.Data
	if d["window"] != 8192 || d["used"].(int) < 100 || d["percent"].(int) < 1 {
		t.Fatalf("estimate: %+v", d)
	}
}
