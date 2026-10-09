package email

import (
	"fmt"
	"net/smtp"
	"strings"
)

// Mailer is the interface for sending emails.
type Mailer interface {
	Send(to, subject, htmlBody, textBody string) error
}

// SMTPMailer sends email via SMTP.
type SMTPMailer struct {
	host      string
	port      string
	username  string
	password  string
	fromEmail string
	fromName  string
}

// NewSMTPMailer creates a new SMTP-backed mailer.
func NewSMTPMailer(host, port, username, password, fromEmail, fromName string) *SMTPMailer {
	return &SMTPMailer{
		host:      host,
		port:      port,
		username:  username,
		password:  password,
		fromEmail: fromEmail,
		fromName:  fromName,
	}
}

// Send transmits an email via SMTP with both HTML and plain-text parts.
// When SMTP_USERNAME is empty (e.g. smtp4dev in local dev), auth is skipped so
// that net/smtp doesn't reject the unauthenticated plaintext connection.
func (m *SMTPMailer) Send(to, subject, htmlBody, textBody string) error {
	var auth smtp.Auth
	if m.username != "" {
		auth = smtp.PlainAuth("", m.username, m.password, m.host)
	}
	addr := fmt.Sprintf("%s:%s", m.host, m.port)

	from := fmt.Sprintf("%s <%s>", m.fromName, m.fromEmail)

	boundary := "==boundary=="
	var msg strings.Builder
	fmt.Fprintf(&msg, "From: %s\r\n", from)
	fmt.Fprintf(&msg, "To: %s\r\n", to)
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	msg.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&msg, "Content-Type: multipart/alternative; boundary=%q\r\n", boundary)
	msg.WriteString("\r\n")

	// Plain-text part
	fmt.Fprintf(&msg, "--%s\r\n", boundary)
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	msg.WriteString(textBody)
	msg.WriteString("\r\n")

	// HTML part
	fmt.Fprintf(&msg, "--%s\r\n", boundary)
	msg.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	msg.WriteString(htmlBody)
	msg.WriteString("\r\n")

	fmt.Fprintf(&msg, "--%s--\r\n", boundary)

	if err := smtp.SendMail(addr, auth, m.fromEmail, []string{to}, []byte(msg.String())); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

// NoopMailer discards all emails. Useful for testing.
type NoopMailer struct{}

func (n *NoopMailer) Send(to, subject, htmlBody, textBody string) error {
	return nil
}
