package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// EmailChannel sends notifications over SMTP.
//
// Two transport security modes are supported: STARTTLS (upgrade a plaintext
// connection, the common case on port 587) and implicit TLS (TLS from the
// first byte, the common case on port 465).
type EmailChannel struct {
	Host        string
	Port        int
	StartTLS    bool
	ImplicitTLS bool
	From        string
	To          []string
	Username    string
	Password    string

	// Timeout bounds the whole conversation. Defaults to 30s.
	Timeout time.Duration
	// HelloName is the EHLO name; defaults to "localhost".
	HelloName string
}

var _ Channel = (*EmailChannel)(nil)

// Type implements Channel.
func (c *EmailChannel) Type() string { return string(apitypes.ChannelEmail) }

// NewEmail builds an EmailChannel from a stored config map. The bool keys
// (starttls, implicit_tls) are strings because channel configs are string maps.
func NewEmail(cfg map[string]string) (*EmailChannel, error) {
	c := &EmailChannel{
		Host:     strings.TrimSpace(cfg[apitypes.ConfigSMTPHost]),
		From:     strings.TrimSpace(cfg[apitypes.ConfigFrom]),
		Username: strings.TrimSpace(cfg[apitypes.ConfigUsername]),
		Password: cfg[apitypes.ConfigPassword],
		To:       SplitList(cfg[apitypes.ConfigTo]),
	}
	if c.Host == "" {
		return nil, ErrConfig(string(apitypes.ChannelEmail), apitypes.ConfigSMTPHost)
	}
	if c.From == "" {
		return nil, ErrConfig(string(apitypes.ChannelEmail), apitypes.ConfigFrom)
	}
	if len(c.To) == 0 {
		return nil, ErrConfig(string(apitypes.ChannelEmail), apitypes.ConfigTo)
	}

	c.Port = 587
	if raw := strings.TrimSpace(cfg[apitypes.ConfigSMTPPort]); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("notify: invalid smtp_port %q", raw)
		}
		c.Port = port
	}

	c.ImplicitTLS = truthy(cfg[apitypes.ConfigImplicitTLS])
	c.StartTLS = truthy(cfg[apitypes.ConfigSTARTTLS])
	if c.ImplicitTLS && c.StartTLS {
		return nil, fmt.Errorf("notify: email channel cannot use starttls and implicit_tls together")
	}
	// Apply a default only when the operator expressed no preference at all:
	// an explicit "false" is a choice and must not be overridden. This matters
	// for plaintext relays and for the test listener.
	_, startTLSSet := cfg[apitypes.ConfigSTARTTLS]
	_, implicitTLSSet := cfg[apitypes.ConfigImplicitTLS]
	if !c.ImplicitTLS && !c.StartTLS && !startTLSSet && !implicitTLSSet {
		if c.Port == 465 {
			c.ImplicitTLS = true
		} else {
			c.StartTLS = true
		}
	}
	c.Timeout = 30 * time.Second
	c.HelloName = "localhost"
	return c, nil
}

// Send implements Channel.
func (c *EmailChannel) Send(ctx context.Context, msg Message) error {
	body, err := c.render(msg)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	dialer := &net.Dialer{Timeout: c.timeout()}

	var conn net.Conn
	if c.ImplicitTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			ServerName: c.Host,
			MinVersion: tls.VersionTLS12,
		})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("notify: dial smtp %s: %w", addr, err)
	}

	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("notify: smtp handshake with %s: %w", addr, err)
	}
	defer client.Close()

	if c.HelloName != "" {
		if err := client.Hello(c.HelloName); err != nil {
			return fmt.Errorf("notify: smtp EHLO: %w", err)
		}
	}

	if c.StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("notify: smtp server %s does not offer STARTTLS", addr)
		}
		if err := client.StartTLS(&tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("notify: smtp STARTTLS: %w", err)
		}
	}

	if c.Username != "" || c.Password != "" {
		if err := c.authenticate(client); err != nil {
			return err
		}
	}

	if err := client.Mail(extractAddress(c.From)); err != nil {
		return fmt.Errorf("notify: smtp MAIL FROM: %w", err)
	}
	for _, to := range c.To {
		if err := client.Rcpt(extractAddress(to)); err != nil {
			return fmt.Errorf("notify: smtp RCPT TO %s: %w", to, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("notify: smtp DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		w.Close()
		return fmt.Errorf("notify: smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notify: smtp finish body: %w", err)
	}
	return client.Quit()
}

func (c *EmailChannel) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 30 * time.Second
	}
	return c.Timeout
}

// authenticate picks PLAIN or LOGIN depending on what the server advertises.
func (c *EmailChannel) authenticate(client *smtp.Client) error {
	ok, mechs := client.Extension("AUTH")
	if !ok {
		return fmt.Errorf("notify: smtp server offered no AUTH mechanism")
	}
	upper := strings.ToUpper(mechs)

	if strings.Contains(upper, "PLAIN") {
		// PlainAuth refuses to send credentials over an unencrypted
		// connection unless the server is localhost, which is exactly the
		// behaviour we want.
		if err := client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return fmt.Errorf("notify: smtp PLAIN auth: %w", err)
		}
		return nil
	}
	if strings.Contains(upper, "LOGIN") {
		if err := client.Auth(&loginAuth{username: c.Username, password: c.Password}); err != nil {
			return fmt.Errorf("notify: smtp LOGIN auth: %w", err)
		}
		return nil
	}
	return fmt.Errorf("notify: smtp server offered no supported AUTH mechanism (advertised %q)", mechs)
}

// render builds a multipart/alternative message with text and HTML parts.
func (c *EmailChannel) render(msg Message) ([]byte, error) {
	subject := msg.Title
	if strings.TrimSpace(subject) == "" {
		subject = "dit notification"
	}

	boundary := fmt.Sprintf("dit-%d", time.Now().UnixNano())
	var b strings.Builder

	writeHeader := func(k, v string) {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteString("\r\n")
	}

	writeHeader("From", c.From)
	writeHeader("To", strings.Join(c.To, ", "))
	writeHeader("Subject", mime.QEncoding.Encode("utf-8", subject))
	writeHeader("Date", time.Now().Format(time.RFC1123Z))
	writeHeader("MIME-Version", "1.0")
	writeHeader("Content-Type", "multipart/alternative; boundary="+boundary)
	b.WriteString("\r\n")

	text := msg.Body
	if msg.URL != "" {
		text = strings.TrimRight(text, "\n") + "\n\n" + msg.URL + "\n"
	}

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(text)
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(renderHTML(msg))
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String()), nil
}

func renderHTML(msg Message) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><body style=\"font-family:system-ui,sans-serif\">")
	b.WriteString("<h2 style=\"margin:0 0 12px\">" + escapeHTML(msg.Title) + "</h2>")
	b.WriteString("<pre style=\"white-space:pre-wrap;font-family:ui-monospace,monospace\">" +
		escapeHTML(msg.Body) + "</pre>")
	if msg.URL != "" {
		b.WriteString(`<p><a href="` + escapeHTML(msg.URL) + `">` + escapeHTML(msg.URL) + "</a></p>")
	}
	if len(msg.Tags) > 0 {
		b.WriteString("<p style=\"color:#666\">" + escapeHTML(strings.Join(msg.Tags, ", ")) + "</p>")
	}
	b.WriteString("</body></html>")
	return b.String()
}

func escapeHTML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;",
	)
	return replacer.Replace(s)
}

// extractAddress strips a display name: `Dit <a@b>` becomes `a@b`.
func extractAddress(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "<"); i >= 0 && strings.HasSuffix(s, ">") {
		return strings.TrimSpace(s[i+1 : len(s)-1])
	}
	return s
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "y":
		return true
	default:
		return false
	}
}

// loginAuth implements the non-standard but widely deployed LOGIN mechanism.
type loginAuth struct {
	username string
	password string
}

func (a *loginAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.username), nil
	case "password:":
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("notify: unexpected LOGIN challenge %q", string(fromServer))
	}
}
