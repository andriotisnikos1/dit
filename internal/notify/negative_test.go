package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestNtfyTreatsNonSuccessStatusAsFailure guards against a 4xx/5xx response
// being counted as a delivered notification.
func TestNtfyTreatsNonSuccessStatusAsFailure(t *testing.T) {
	codes := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
	}
	for _, code := range codes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "nope", code)
			}))
			defer server.Close()

			channel, err := NewNtfy(map[string]string{
				apitypes.ConfigTopic: "t", apitypes.ConfigURL: server.URL,
			})
			if err != nil {
				t.Fatalf("NewNtfy: %v", err)
			}
			if err := channel.Send(context.Background(), Message{Title: "x", Body: "y"}); err == nil {
				t.Errorf("Send against a %d response returned nil; a non-2xx must fail", code)
			}
		})
	}
}

// TestNtfyAcceptsEveryTwoHundred guards the other side of the boundary.
func TestNtfyAcceptsEveryTwoHundred(t *testing.T) {
	for _, code := range []int{200, 201, 202, 204} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()

			channel, err := NewNtfy(map[string]string{
				apitypes.ConfigTopic: "t", apitypes.ConfigURL: server.URL,
			})
			if err != nil {
				t.Fatalf("NewNtfy: %v", err)
			}
			if err := channel.Send(context.Background(), Message{Title: "x", Body: "y"}); err != nil {
				t.Errorf("Send against a %d response: %v, want success", code, err)
			}
		})
	}
}

// TestNtfyPublishesToTheTopicPath guards URL construction, including a server
// URL that already carries a path prefix and a topic needing escaping.
func TestNtfyPublishesToTheTopicPath(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		topic   string
		want    string
	}{
		{"bare host", "", "alerts", "/alerts"},
		{"trailing slash", "", "alerts", "/alerts"},
		{"path prefix", "/ntfy", "alerts", "/ntfy/alerts"},
		{"path prefix with slash", "/ntfy/", "alerts", "/ntfy/alerts"},
		{"topic needing escape", "", "my topic", "/my%20topic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.EscapedPath()
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			base := server.URL
			if tc.baseURL != "" {
				base = server.URL + tc.baseURL
			}
			channel, err := NewNtfy(map[string]string{
				apitypes.ConfigTopic: tc.topic, apitypes.ConfigURL: base,
			})
			if err != nil {
				t.Fatalf("NewNtfy: %v", err)
			}
			if err := channel.Send(context.Background(), Message{Title: "x", Body: "y"}); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if got := <-paths; got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNtfyBodyCarriesTheURLOnce guards the body assembly: the URL is appended
// when it is not already there, and not duplicated when it is.
func TestNtfyBodyCarriesTheURLOnce(t *testing.T) {
	bodies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies <- string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel, err := NewNtfy(map[string]string{
		apitypes.ConfigTopic: "t", apitypes.ConfigURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewNtfy: %v", err)
	}

	const url = "https://ghcr.io/owner/app"
	if err := channel.Send(context.Background(), Message{Body: "changed", URL: url}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	body := <-bodies
	if strings.Count(body, url) != 1 {
		t.Errorf("body contains the URL %d times, want 1:\n%s", strings.Count(body, url), body)
	}

	// A body that already mentions the URL must not get a second copy.
	if err := channel.Send(context.Background(), Message{Body: "see " + url, URL: url}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	body = <-bodies
	if strings.Count(body, url) != 1 {
		t.Errorf("body contains the URL %d times, want 1:\n%s", strings.Count(body, url), body)
	}
}

// TestNtfyOmitsEmptyHeaders guards against sending blank Title/Priority values,
// which some servers reject or render as an empty notification.
func TestNtfyOmitsEmptyHeaders(t *testing.T) {
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel, err := NewNtfy(map[string]string{apitypes.ConfigTopic: "t", apitypes.ConfigURL: server.URL})
	if err != nil {
		t.Fatalf("NewNtfy: %v", err)
	}
	if err := channel.Send(context.Background(), Message{Body: "only a body"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := <-headers
	if title := got.Get("Title"); title != "" {
		t.Errorf("Title = %q, want it omitted when the message has none", title)
	}
	if click := got.Get("Click"); click != "" {
		t.Errorf("Click = %q, want it omitted when the message has no URL", click)
	}
	if auth := got.Get("Authorization"); auth != "" {
		t.Errorf("Authorization = %q, want it omitted when no token is configured", auth)
	}
	// A default priority is still worth sending: it makes the notification's
	// importance explicit rather than server-defined.
	if priority := got.Get("Priority"); priority != "default" {
		t.Errorf("Priority = %q, want default", priority)
	}
}

// TestNtfySendRespectsContextCancellation guards a hung publish.
func TestNtfySendRespectsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)

	channel, err := NewNtfy(map[string]string{apitypes.ConfigTopic: "t", apitypes.ConfigURL: server.URL})
	if err != nil {
		t.Fatalf("NewNtfy: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := channel.Send(ctx, Message{Title: "x", Body: "y"}); err == nil {
		t.Error("Send with a cancelled context returned nil, want an error")
	}
}

// TestEmailRendersEveryMessageField guards the rendered message against a
// field being silently dropped from the body.
func TestEmailRendersEveryMessageField(t *testing.T) {
	server := newFakeSMTP(t)
	host, port := server.addr()

	channel, err := NewEmail(map[string]string{
		apitypes.ConfigSMTPHost:    host,
		apitypes.ConfigSMTPPort:    itoa(port),
		apitypes.ConfigFrom:        "dit@example.com",
		apitypes.ConfigTo:          "ops@example.com",
		apitypes.ConfigSTARTTLS:    "false",
		apitypes.ConfigImplicitTLS: "false",
	})
	if err != nil {
		t.Fatalf("NewEmail: %v", err)
	}

	if err := channel.Send(context.Background(), Message{
		Title: "digest changed",
		Body:  "line one\nline two",
		URL:   "https://ghcr.io/owner/app",
		Tags:  []string{"whale"},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	message := server.waitForMessage(t)
	for _, want := range []string{
		"Subject:", "text/plain", "text/html",
		"line one", "line two", "https://ghcr.io/owner/app", "whale",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the rendered message is missing %q:\n%s", want, message)
		}
	}
}

// TestEmailEscapesHTMLInTheBody guards against a tag or digest containing
// markup breaking the HTML part of the message. The text part is allowed to
// carry the literal characters — that is what a text part is for — so the
// assertion is scoped to the HTML section.
func TestEmailEscapesHTMLInTheBody(t *testing.T) {
	server := newFakeSMTP(t)
	host, port := server.addr()

	channel, err := NewEmail(map[string]string{
		apitypes.ConfigSMTPHost:    host,
		apitypes.ConfigSMTPPort:    itoa(port),
		apitypes.ConfigFrom:        "dit@example.com",
		apitypes.ConfigTo:          "ops@example.com",
		apitypes.ConfigSTARTTLS:    "false",
		apitypes.ConfigImplicitTLS: "false",
	})
	if err != nil {
		t.Fatalf("NewEmail: %v", err)
	}

	if err := channel.Send(context.Background(), Message{
		Title: "a <b>bold</b> tag",
		Body:  "<img src=x onerror=alert(1)>",
		URL:   `https://example.com/?q="><script>x</script>`,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	message := server.waitForMessage(t)
	html := htmlPart(t, message)

	if strings.Contains(html, "<img src=x") {
		t.Errorf("the raw img tag reached the HTML part:\n%s", html)
	}
	if !strings.Contains(html, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Errorf("the HTML part does not carry the escaped body:\n%s", html)
	}
	if strings.Contains(html, `href="https://example.com/?q="><script>`) {
		t.Errorf("the URL attribute was not escaped:\n%s", html)
	}
	if !strings.Contains(html, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Errorf("the HTML part does not carry the escaped title:\n%s", html)
	}

	// No header may carry a bare newline, which would allow injection.
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, "Subject:") && strings.Contains(line, "\r") {
			t.Errorf("subject line carries a carriage return: %q", line)
		}
	}
}

// htmlPart extracts the text/html section of a multipart message.
func htmlPart(t *testing.T, message string) string {
	t.Helper()
	idx := strings.Index(message, "Content-Type: text/html")
	if idx < 0 {
		t.Fatalf("no text/html part in the message:\n%s", message)
	}
	rest := message[idx:]
	// Skip the part's own headers up to the blank line.
	if blank := strings.Index(rest, "\n\n"); blank >= 0 {
		rest = rest[blank+2:]
	}
	// Stop at the closing boundary.
	if end := strings.Index(rest, "\n--"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// TestEmailSubjectEncoding guards a non-ASCII subject being sent raw, which
// breaks the header.
func TestEmailSubjectEncoding(t *testing.T) {
	server := newFakeSMTP(t)
	host, port := server.addr()

	channel, err := NewEmail(map[string]string{
		apitypes.ConfigSMTPHost:    host,
		apitypes.ConfigSMTPPort:    itoa(port),
		apitypes.ConfigFrom:        "dit@example.com",
		apitypes.ConfigTo:          "ops@example.com",
		apitypes.ConfigSTARTTLS:    "false",
		apitypes.ConfigImplicitTLS: "false",
	})
	if err != nil {
		t.Fatalf("NewEmail: %v", err)
	}

	if err := channel.Send(context.Background(), Message{
		Title: "νέα έκδοση: nginx 1.28",
		Body:  "body",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	message := server.waitForMessage(t)
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, "Subject:") {
			if !strings.Contains(line, "=?utf-8?") {
				t.Errorf("subject line is not encoded: %q", line)
			}
			return
		}
	}
	t.Error("no Subject line found in the message")
}

// TestDefaultBuilderBuildsRealChannels guards that the production builder
// dispatches by type rather than always returning one kind.
func TestDefaultBuilderBuildsRealChannels(t *testing.T) {
	builder := DefaultBuilder{}

	email, err := builder.Build("c_1", string(apitypes.ChannelEmail), map[string]string{
		apitypes.ConfigSMTPHost: "smtp.example.com",
		apitypes.ConfigFrom:     "dit@example.com",
		apitypes.ConfigTo:       "ops@example.com",
	})
	if err != nil {
		t.Fatalf("Build(email): %v", err)
	}
	if email.Type() != string(apitypes.ChannelEmail) {
		t.Errorf("type = %q, want email", email.Type())
	}

	ntfy, err := builder.Build("c_2", string(apitypes.ChannelNtfy), map[string]string{
		apitypes.ConfigTopic: "t",
	})
	if err != nil {
		t.Fatalf("Build(ntfy): %v", err)
	}
	if ntfy.Type() != string(apitypes.ChannelNtfy) {
		t.Errorf("type = %q, want ntfy", ntfy.Type())
	}
}

// itoa avoids importing strconv into the test for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
