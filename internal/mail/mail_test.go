// SPDX-License-Identifier: GPL-2.0-or-later

package mail

import (
	"context"
	"net/smtp"
	"strings"
	"testing"
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

func TestSMTPSend(t *testing.T) {
	var gotAddr, gotFrom string
	var gotTo []string
	s := &SMTP{Host: "smtp.example.com", Port: 587, User: "u", Password: "p", From: "from@example.com",
		send: func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
			gotAddr, gotFrom, gotTo = addr, from, to
			if a == nil {
				t.Error("auth expected")
			}
			return nil
		}}
	if err := s.Send(context.Background(), Message{To: "to@example.com", Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if gotAddr != "smtp.example.com:587" || gotFrom != "from@example.com" || gotTo[0] != "to@example.com" {
		t.Fatalf("%s %s %v", gotAddr, gotFrom, gotTo)
	}
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
