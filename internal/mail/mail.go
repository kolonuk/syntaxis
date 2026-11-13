// SPDX-License-Identifier: GPL-2.0-or-later

// Package mail sends email via SMTP or, in dev mode, logs it.
package mail

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
)

// Message is an outbound email.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Mailer sends messages.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// Console logs messages instead of sending them (dev mode).
type Console struct {
	Log *slog.Logger
}

// Send logs the message, including its body so invite links are usable in dev.
func (c Console) Send(_ context.Context, m Message) error {
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	log.Info("email (dev mode, not sent)", "to", m.To, "subject", m.Subject, "body", m.Body)
	return nil
}

// SMTP sends mail through an SMTP relay using STARTTLS when offered.
type SMTP struct {
	Host, User, Password, From string
	Port                       int
	// send is overridable in tests.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// ErrHeaderInjection is returned when a header value contains CR/LF.
var ErrHeaderInjection = fmt.Errorf("mail: header injection attempt")

// Send delivers the message.
func (s *SMTP) Send(_ context.Context, m Message) error {
	raw, err := Format(s.From, m)
	if err != nil {
		return err
	}
	var auth smtp.Auth
	if s.User != "" {
		auth = smtp.PlainAuth("", s.User, s.Password, s.Host)
	}
	send := s.send
	if send == nil {
		send = smtp.SendMail
	}
	return send(net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), auth, s.From, []string{m.To}, raw)
}

// Format renders an RFC 5322 message, rejecting header injection.
func Format(from string, m Message) ([]byte, error) {
	for _, v := range []string{from, m.To, m.Subject} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, ErrHeaderInjection
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\n", from, m.To, m.Subject)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(m.Body, "\n", "\r\n"))
	return []byte(b.String()), nil
}

// Recorder keeps sent messages in memory (tests).
type Recorder struct {
	mu   sync.Mutex
	Sent []Message
}

// Send records the message.
func (r *Recorder) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Sent = append(r.Sent, m)
	return nil
}

// Last returns the most recent message.
func (r *Recorder) Last() (Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.Sent) == 0 {
		return Message{}, false
	}
	return r.Sent[len(r.Sent)-1], true
}
