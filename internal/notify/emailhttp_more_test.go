package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// The new type must survive the same guarantees the existing types have:
// validation at construction, and a real send reaching the provider.

func TestEmailHTTPFullURLCompositionPerProvider(t *testing.T) {
	// A base URL with a trailing slash must not produce a doubled separator.
	cases := map[string]string{
		"resend":   "/emails",
		"postmark": "/email",
		"sendgrid": "/v3/mail/send",
	}
	for provider, wantPath := range cases {
		t.Run(provider, func(t *testing.T) {
			server, captured := newCaptureServer(t, http.StatusOK, `{}`)
			cfg := httpConfig(provider, server.URL+"/") // trailing slash
			channel, err := NewEmailHTTP(cfg)
			if err != nil {
				t.Fatalf("NewEmailHTTP: %v", err)
			}
			if err := channel.Send(context.Background(), Message{Title: "t", Body: "b"}); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if captured.Path != wantPath {
				t.Errorf("path = %q, want %q (no doubled slash)", captured.Path, wantPath)
			}
		})
	}
}

func TestEmailHTTPMultipleRecipients(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, `{}`)
	cfg := httpConfig("resend", server.URL)
	cfg[apitypes.ConfigTo] = "a@example.com, b@example.com"
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}
	if err := channel.Send(context.Background(), Message{Title: "t", Body: "b"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(string(captured.Body), "a@example.com") ||
		!strings.Contains(string(captured.Body), "b@example.com") {
		t.Errorf("both recipients should be in the body: %s", captured.Body)
	}
}

// A 202 is a success for some providers; only 2xx in general must pass.
func TestEmailHTTPAcceptStatusRange(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted} {
		server, _ := newCaptureServer(t, status, `{}`)
		cfg := httpConfig("resend", server.URL)
		channel, err := NewEmailHTTP(cfg)
		if err != nil {
			t.Fatalf("NewEmailHTTP: %v", err)
		}
		if err := channel.Send(context.Background(), Message{Title: "t", Body: "b"}); err != nil {
			t.Errorf("status %d should be accepted: %v", status, err)
		}
	}
}

func TestEmailHTTPSecretIsNotInErrorText(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusForbidden, `{"error":"denied"}`)
	cfg := httpConfig("resend", server.URL)
	cfg[apitypes.ConfigAPIKey] = "super-secret-value"
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}
	err = channel.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "super-secret-value") {
		t.Errorf("the API key leaked into the error: %v", err)
	}
}

// A provider that hangs must not wedge the caller: the send is bounded by the
// channel's own timeout rather than waiting forever.
func TestEmailHTTPHonoursTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	cfg := httpConfig("resend", server.URL)
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}
	channel.Timeout = 250 * time.Millisecond

	start := time.Now()
	err = channel.Send(context.Background(), Message{Title: "t", Body: "b"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a hanging provider must produce an error, not a silent success")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Send took %s, the timeout did not bound it", elapsed)
	}
}
