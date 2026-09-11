package notify

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func TestNewNtfyDefaultsAndValidation(t *testing.T) {
	channel, err := NewNtfy(map[string]string{apitypes.ConfigTopic: "alerts"})
	if err != nil {
		t.Fatalf("NewNtfy: %v", err)
	}
	if channel.URL != DefaultNtfyURL {
		t.Errorf("URL = %q, want the default %q", channel.URL, DefaultNtfyURL)
	}
	if channel.Priority != "default" {
		t.Errorf("Priority = %q, want default", channel.Priority)
	}

	if _, err := NewNtfy(map[string]string{}); err == nil {
		t.Error("NewNtfy without a topic: expected an error")
	}
	if _, err := NewNtfy(map[string]string{
		apitypes.ConfigTopic: "t", apitypes.ConfigURL: "://bad",
	}); err == nil {
		t.Error("NewNtfy with a malformed URL: expected an error")
	}
}

func TestNtfySendPublishesHeadersAndBody(t *testing.T) {
	type captured struct {
		method  string
		path    string
		headers http.Header
		body    string
	}
	requests := make(chan captured, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		requests <- captured{method: r.Method, path: r.URL.Path, headers: r.Header.Clone(), body: string(body)}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel, err := NewNtfy(map[string]string{
		apitypes.ConfigTopic:    "dit-alerts",
		apitypes.ConfigURL:      server.URL,
		apitypes.ConfigToken:    "tk_secret",
		apitypes.ConfigPriority: "high",
		apitypes.ConfigTags:     "whale,package",
	})
	if err != nil {
		t.Fatalf("NewNtfy: %v", err)
	}

	err = channel.Send(context.Background(), Message{
		Title: "digest changed: nginx:1.27",
		Body:  "new digest sha256:abc",
		URL:   "https://index.docker.io/library/nginx",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-requests:
		if got.method != http.MethodPost {
			t.Errorf("method = %s, want POST", got.method)
		}
		if got.path != "/dit-alerts" {
			t.Errorf("path = %s, want /dit-alerts", got.path)
		}
		if title := got.headers.Get("Title"); title != "digest changed: nginx:1.27" {
			t.Errorf("Title header = %q", title)
		}
		if priority := got.headers.Get("Priority"); priority != "high" {
			t.Errorf("Priority header = %q, want high", priority)
		}
		if tags := got.headers.Get("Tags"); tags != "whale,package" {
			t.Errorf("Tags header = %q, want whale,package", tags)
		}
		if click := got.headers.Get("Click"); click != "https://index.docker.io/library/nginx" {
			t.Errorf("Click header = %q", click)
		}
		if auth := got.headers.Get("Authorization"); auth != "Bearer tk_secret" {
			t.Errorf("Authorization header = %q, want Bearer tk_secret", auth)
		}
		if !strings.Contains(got.body, "sha256:abc") {
			t.Errorf("body = %q, want it to carry the message body", got.body)
		}
		// The URL must be appended when it is not already in the body.
		if !strings.Contains(got.body, "https://index.docker.io/library/nginx") {
			t.Errorf("body = %q, want it to carry the URL", got.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the ntfy server received no request")
	}
}

func TestNtfySendReportsHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "topic is reserved", http.StatusBadRequest)
	}))
	defer server.Close()

	channel, err := NewNtfy(map[string]string{apitypes.ConfigTopic: "t", apitypes.ConfigURL: server.URL})
	if err != nil {
		t.Fatalf("NewNtfy: %v", err)
	}
	err = channel.Send(context.Background(), Message{Title: "x", Body: "y"})
	if err == nil {
		t.Fatal("Send against a 400 response: expected an error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %v, want it to mention the status code", err)
	}
}

func TestNtfySendStripsNewlinesFromTitle(t *testing.T) {
	titles := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		titles <- r.Header.Get("Title")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel, err := NewNtfy(map[string]string{apitypes.ConfigTopic: "t", apitypes.ConfigURL: server.URL})
	if err != nil {
		t.Fatalf("NewNtfy: %v", err)
	}
	// A newline in a header value would make the request malformed.
	if err := channel.Send(context.Background(), Message{Title: "line one\nline two", Body: "b"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case title := <-titles:
		if strings.ContainsAny(title, "\r\n") {
			t.Errorf("Title header %q still contains a newline", title)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the ntfy server received no request")
	}
}

func TestNormalisePriority(t *testing.T) {
	cases := map[string]string{
		"":        "default",
		"default": "default",
		"min":     "low",
		"low":     "low",
		"high":    "high",
		"max":     "urgent",
		"urgent":  "urgent",
		"banana":  "default",
	}
	for input, want := range cases {
		if got := NormalisePriority(input); got != want {
			t.Errorf("NormalisePriority(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildDispatch(t *testing.T) {
	email, err := Build(string(apitypes.ChannelEmail), map[string]string{
		apitypes.ConfigSMTPHost: "smtp.example.com",
		apitypes.ConfigFrom:     "dit@example.com",
		apitypes.ConfigTo:       "ops@example.com",
	})
	if err != nil {
		t.Fatalf("Build(email): %v", err)
	}
	if email.Type() != string(apitypes.ChannelEmail) {
		t.Errorf("email Type() = %q", email.Type())
	}

	ntfy, err := Build(string(apitypes.ChannelNtfy), map[string]string{apitypes.ConfigTopic: "t"})
	if err != nil {
		t.Fatalf("Build(ntfy): %v", err)
	}
	if ntfy.Type() != string(apitypes.ChannelNtfy) {
		t.Errorf("ntfy Type() = %q", ntfy.Type())
	}

	if _, err := Build("carrier-pigeon", nil); err == nil {
		t.Error("Build with an unknown type: expected an error")
	}
}

func TestSplitAndJoinList(t *testing.T) {
	got := SplitList("a, b;c  d")
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("SplitList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SplitList[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if out := JoinList(want); out != "a,b,c,d" {
		t.Errorf("JoinList = %q, want a,b,c,d", out)
	}
}

// ---------- email against a fake SMTP listener ----------

// fakeSMTP is a minimal SMTP server that accepts one message and records it.
type fakeSMTP struct {
	listener net.Listener
	mu       sync.Mutex
	messages []string
	auth     string
	ready    chan struct{}
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &fakeSMTP{listener: listener, ready: make(chan struct{}, 8)}
	t.Cleanup(func() { _ = listener.Close() })

	go server.serve()
	return server
}

func (s *fakeSMTP) addr() (host string, port int) {
	addr := s.listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func (s *fakeSMTP) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	send := func(line string) {
		_, _ = writer.WriteString(line + "\r\n")
		_ = writer.Flush()
	}

	send("220 fake ESMTP ready")
	inData := false
	var body strings.Builder

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		if inData {
			if line == "." {
				inData = false
				s.mu.Lock()
				s.messages = append(s.messages, body.String())
				s.mu.Unlock()
				body.Reset()
				send("250 2.0.0 Ok: queued")
				s.ready <- struct{}{}
				continue
			}
			body.WriteString(line + "\n")
			continue
		}

		switch {
		case strings.HasPrefix(strings.ToUpper(line), "EHLO"), strings.HasPrefix(strings.ToUpper(line), "HELO"):
			send("250-fake greets you")
			send("250-AUTH PLAIN LOGIN")
			send("250 8BITMIME")
		case strings.HasPrefix(strings.ToUpper(line), "AUTH"):
			s.mu.Lock()
			s.auth = line
			s.mu.Unlock()
			send("235 2.7.0 Authentication successful")
		case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM"):
			send("250 2.1.0 Ok")
		case strings.HasPrefix(strings.ToUpper(line), "RCPT TO"):
			send("250 2.1.5 Ok")
		case strings.EqualFold(line, "DATA"):
			inData = true
			send("354 End data with <CR><LF>.<CR><LF>")
		case strings.EqualFold(line, "QUIT"):
			send("221 2.0.0 Bye")
			return
		case strings.EqualFold(line, "RSET"):
			send("250 2.0.0 Ok")
		default:
			send("250 2.0.0 Ok")
		}
	}
}

func (s *fakeSMTP) waitForMessage(t *testing.T) string {
	t.Helper()
	select {
	case <-s.ready:
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.messages) == 0 {
			t.Fatal("the SMTP server recorded no message")
		}
		return s.messages[len(s.messages)-1]
	case <-time.After(5 * time.Second):
		t.Fatal("the SMTP server received no message")
		return ""
	}
}

func TestEmailSendDeliversMultipartMessage(t *testing.T) {
	server := newFakeSMTP(t)
	host, port := server.addr()

	channel, err := NewEmail(map[string]string{
		apitypes.ConfigSMTPHost:    host,
		apitypes.ConfigSMTPPort:    fmt.Sprintf("%d", port),
		apitypes.ConfigFrom:        "dit@example.com",
		apitypes.ConfigTo:          "ops@example.com,oncall@example.com",
		apitypes.ConfigSTARTTLS:    "false",
		apitypes.ConfigImplicitTLS: "false",
	})
	if err != nil {
		t.Fatalf("NewEmail: %v", err)
	}
	if channel.StartTLS {
		t.Error("StartTLS = true, want false when explicitly disabled")
	}
	if channel.ImplicitTLS {
		t.Error("ImplicitTLS = true, want false")
	}
	if len(channel.To) != 2 {
		t.Errorf("To = %v, want 2 recipients", channel.To)
	}

	if err := channel.Send(context.Background(), Message{
		Title: "new tags: ghcr.io/owner/app",
		Body:  "v1.2.0\nv1.3.0",
		URL:   "https://ghcr.io/owner/app",
		Tags:  []string{"package"},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	message := server.waitForMessage(t)
	for _, want := range []string{
		"Subject:", "multipart/alternative", "text/plain", "text/html",
		"v1.2.0", "https://ghcr.io/owner/app", "To: ops@example.com, oncall@example.com",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message is missing %q:\n%s", want, message)
		}
	}
}

func TestEmailSendAuthenticates(t *testing.T) {
	server := newFakeSMTP(t)
	host, port := server.addr()

	channel, err := NewEmail(map[string]string{
		apitypes.ConfigSMTPHost:    host,
		apitypes.ConfigSMTPPort:    fmt.Sprintf("%d", port),
		apitypes.ConfigFrom:        "dit@example.com",
		apitypes.ConfigTo:          "ops@example.com",
		apitypes.ConfigUsername:    "dit",
		apitypes.ConfigPassword:    "smtp-password",
		apitypes.ConfigSTARTTLS:    "false",
		apitypes.ConfigImplicitTLS: "false",
	})
	if err != nil {
		t.Fatalf("NewEmail: %v", err)
	}
	if err := channel.Send(context.Background(), Message{Title: "t", Body: "b"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	server.waitForMessage(t)

	server.mu.Lock()
	auth := server.auth
	server.mu.Unlock()
	if !strings.HasPrefix(strings.ToUpper(auth), "AUTH") {
		t.Errorf("the client did not authenticate; recorded %q", auth)
	}
}

func TestEmailSendReportsUnreachableServer(t *testing.T) {
	// Reserve a port and close it, so nothing is listening.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	channel, err := NewEmail(map[string]string{
		apitypes.ConfigSMTPHost: "127.0.0.1",
		apitypes.ConfigSMTPPort: fmt.Sprintf("%d", port),
		apitypes.ConfigFrom:     "dit@example.com",
		apitypes.ConfigTo:       "ops@example.com",
	})
	if err != nil {
		t.Fatalf("NewEmail: %v", err)
	}
	channel.Timeout = 2 * time.Second
	if err := channel.Send(context.Background(), Message{Title: "t", Body: "b"}); err == nil {
		t.Error("Send to a closed port: expected an error")
	}
}

func TestNewEmailValidation(t *testing.T) {
	cases := []map[string]string{
		{apitypes.ConfigFrom: "a@b.c", apitypes.ConfigTo: "d@e.f"},   // no host
		{apitypes.ConfigSMTPHost: "h", apitypes.ConfigTo: "d@e.f"},   // no from
		{apitypes.ConfigSMTPHost: "h", apitypes.ConfigFrom: "a@b.c"}, // no to
		{apitypes.ConfigSMTPHost: "h", apitypes.ConfigFrom: "a@b.c", apitypes.ConfigTo: "d@e.f",
			apitypes.ConfigSMTPPort: "not-a-port"}, // bad port
		{apitypes.ConfigSMTPHost: "h", apitypes.ConfigFrom: "a@b.c", apitypes.ConfigTo: "d@e.f",
			apitypes.ConfigSTARTTLS: "true", apitypes.ConfigImplicitTLS: "true"}, // both TLS modes
	}
	for i, cfg := range cases {
		if _, err := NewEmail(cfg); err == nil {
			t.Errorf("case %d: NewEmail accepted an invalid config %v", i, cfg)
		}
	}
}

func TestEmailPort465DefaultsToImplicitTLS(t *testing.T) {
	channel, err := NewEmail(map[string]string{
		apitypes.ConfigSMTPHost: "smtp.example.com",
		apitypes.ConfigSMTPPort: "465",
		apitypes.ConfigFrom:     "dit@example.com",
		apitypes.ConfigTo:       "ops@example.com",
	})
	if err != nil {
		t.Fatalf("NewEmail: %v", err)
	}
	if !channel.ImplicitTLS {
		t.Error("ImplicitTLS = false for port 465, want true")
	}
	if channel.StartTLS {
		t.Error("StartTLS = true for port 465, want false")
	}
}

func TestExtractAddress(t *testing.T) {
	cases := map[string]string{
		"a@b.c":           "a@b.c",
		"Dit <a@b.c>":     "a@b.c",
		"  Dit <a@b.c>  ": "a@b.c",
		"<a@b.c>":         "a@b.c",
	}
	for input, want := range cases {
		if got := extractAddress(input); got != want {
			t.Errorf("extractAddress(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRecordingChannel(t *testing.T) {
	rec := NewRecording(string(apitypes.ChannelNtfy))
	if rec.Type() != string(apitypes.ChannelNtfy) {
		t.Errorf("Type() = %q", rec.Type())
	}
	ctx := context.Background()
	if err := rec.Send(ctx, Message{Title: "one"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := rec.Send(ctx, Message{Title: "two"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if rec.Count() != 2 {
		t.Errorf("Count() = %d, want 2", rec.Count())
	}

	rec.FailFor = func(Message) bool { return true }
	if err := rec.Send(ctx, Message{Title: "three"}); err == nil {
		t.Error("Send with FailFor: expected an error")
	}
	if rec.Count() != 2 {
		t.Errorf("Count() = %d after a failure, want 2", rec.Count())
	}

	rec.Reset()
	if rec.Count() != 0 {
		t.Errorf("Count() = %d after Reset, want 0", rec.Count())
	}
}

func TestFakeBuilder(t *testing.T) {
	builder := NewFakeBuilder()
	first := builder.Set("c_1", string(apitypes.ChannelNtfy))

	channel, err := builder.Build("c_1", string(apitypes.ChannelNtfy), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if channel != Channel(first) {
		t.Error("Build did not return the registered recording channel")
	}

	// An unregistered ID still yields a usable recording channel.
	other, err := builder.Build("c_2", string(apitypes.ChannelEmail), nil)
	if err != nil {
		t.Fatalf("Build for an unregistered ID: %v", err)
	}
	if other == nil {
		t.Error("Build returned a nil channel")
	}

	builder.Fail("c_3", ErrConfig("ntfy", "topic"))
	if _, err := builder.Build("c_3", string(apitypes.ChannelNtfy), nil); err == nil {
		t.Error("Build for a failed ID: expected an error")
	}
}
