// Package mail sends plain-text email over SMTP (for example Gmail with an
// App Password).
package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// Message is one email. HTML is optional; Text is always sent as the
// fallback for mail apps that don't show HTML.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	Headers map[string]string // extra headers such as List-Unsubscribe
}

// Sender sends one email.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

type SMTP struct {
	Host string
	Port string // 587 uses STARTTLS, 465 uses TLS from the start
	User string
	Pass string
	From string // "Name <address>" or a bare address
}

func (s SMTP) Send(ctx context.Context, m Message) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("MAIL_FROM: %w", err)
	}
	msg := buildMessage(s.From, m)
	addr := net.JoinHostPort(s.Host, s.Port)
	auth := smtp.PlainAuth("", s.User, s.Pass, s.Host)

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(15 * time.Second)
	}
	dialer := &net.Dialer{Deadline: deadline}
	var conn net.Conn
	if s.Port == "465" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.Host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if s.Port != "465" {
		if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(m.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMessage encodes the subject and body as UTF-8 so Arabic text survives,
// with an HTML part when there is one.
func buildMessage(from string, m Message) []byte {
	const crlf = "\r\n"
	var b strings.Builder
	b.WriteString("From: " + from + crlf)
	b.WriteString("To: " + m.To + crlf)
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + crlf)
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + crlf)
	for k, v := range m.Headers {
		b.WriteString(k + ": " + v + crlf)
	}
	b.WriteString("MIME-Version: 1.0" + crlf)
	if m.HTML == "" {
		writePart(&b, "text/plain", m.Text)
		return []byte(b.String())
	}
	const boundary = "speakup-alt-boundary"
	b.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + crlf + crlf)
	b.WriteString("--" + boundary + crlf)
	writePart(&b, "text/plain", m.Text)
	b.WriteString("--" + boundary + crlf)
	writePart(&b, "text/html", m.HTML)
	b.WriteString("--" + boundary + "--" + crlf)
	return []byte(b.String())
}

func writePart(b *strings.Builder, contentType, body string) {
	const crlf = "\r\n"
	b.WriteString("Content-Type: " + contentType + "; charset=UTF-8" + crlf)
	b.WriteString("Content-Transfer-Encoding: base64" + crlf + crlf)
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + crlf)
		enc = enc[76:]
	}
	b.WriteString(enc + crlf)
}
