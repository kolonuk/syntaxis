// SPDX-License-Identifier: GPL-2.0-or-later

package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kolonuk/syntaxis/internal/testenv"
)

func TestFormatRejectsInjection(t *testing.T) {
	if _, err := Format("a@example.com", Message{To: "b@example.com\r\nBcc: evil@example.com", Subject: "x"}); err == nil {
		t.Fatal("to injection accepted")
	}
	if _, err := Format("a@example.com", Message{To: "b@example.com", Subject: "x\nBcc: e"}); err == nil {
		t.Fatal("subject injection accepted")
	}
	b, err := Format("a@example.com", Message{To: "b@example.com", Subject: "hi", Body: "l1\nl2"})
	if err != nil || !strings.Contains(string(b), "l1\r\nl2") {
		t.Fatalf("format: %q %v", b, err)
	}
}

// Delivery through a real SMTP server (Mailpit in a container), read back from its own API.
func TestSMTPSendDeliversThroughARealServer(t *testing.T) {
	testenv.Docker(t)
	smtpPort, apiPort := freePort(t), freePort(t)
	name := fmt.Sprintf("syntaxis-test-mailpit-%d", time.Now().UnixNano()%1e9)
	if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:1025", smtpPort), "-p", fmt.Sprintf("127.0.0.1:%d:8025", apiPort), "axllent/mailpit:latest").CombinedOutput(); err != nil {
		testenv.NotTested(t, "the Mailpit image could not be started (no network to pull it?): "+strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	api := fmt.Sprintf("http://127.0.0.1:%d/api/v1/messages", apiPort)
	deadline := time.Now().Add(60 * time.Second)
	for {
		if r, err := http.Get(api); err == nil {
			_ = r.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Mailpit did not start")
		}
		time.Sleep(500 * time.Millisecond)
	}
	s := &SMTP{Host: "127.0.0.1", Port: smtpPort, From: "from@example.com"}
	if err := s.Send(context.Background(), Message{To: "to@example.com", Subject: "Your invite", Body: "Open https://syntaxis.example.com/#token=abc"}); err != nil {
		t.Fatal(err)
	}
	r, err := http.Get(api)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var got struct {
		Messages []struct {
			Subject string
			Snippet string
			From    struct{ Address string }
			To      []struct{ Address string }
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil || len(got.Messages) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	m := got.Messages[0]
	if m.Subject != "Your invite" || m.From.Address != "from@example.com" || len(m.To) != 1 || m.To[0].Address != "to@example.com" || !strings.Contains(m.Snippet, "token=abc") {
		t.Fatalf("%+v", m)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestRecorderAndConsole(t *testing.T) {
	r := &Recorder{}
	if _, ok := r.Last(); ok {
		t.Fatal("empty")
	}
	_ = r.Send(context.Background(), Message{To: "x"})
	if m, ok := r.Last(); !ok || m.To != "x" {
		t.Fatal("last")
	}
	if err := (Console{}).Send(context.Background(), Message{To: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestSMTPAuthAndStartTLS(t *testing.T) {
	testenv.NotTested(t, "SMTP login and STARTTLS against a real mail relay (needs an account on one)")
}
