package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// capturedRequest records what the channel actually put on the wire.
type capturedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// newCaptureServer stands in for a provider API and asserts the shared
// expectations every provider must satisfy.
func newCaptureServer(t *testing.T, status int, body string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.Header = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		captured.Body = raw
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func httpConfig(provider, url string) map[string]string {
	return map[string]string{
		apitypes.ConfigProvider: provider,
		apitypes.ConfigAPIKey:   "key_secret",
		apitypes.ConfigFrom:     "dit@example.com",
		apitypes.ConfigTo:       "ops@example.com",
		apitypes.ConfigURL:      url,
	}
}

func TestNewEmailHTTPValidation(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			apitypes.ConfigProvider: "resend",
			apitypes.ConfigAPIKey:   "k",
			apitypes.ConfigFrom:     "a@b.c",
			apitypes.ConfigTo:       "d@e.f",
		}
	}

	cases := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{"no provider", func(c map[string]string) { delete(c, apitypes.ConfigProvider) }},
		{"unknown provider", func(c map[string]string) { c[apitypes.ConfigProvider] = "nosuch" }},
		{"no api key", func(c map[string]string) { delete(c, apitypes.ConfigAPIKey) }},
		{"no from", func(c map[string]string) { delete(c, apitypes.ConfigFrom) }},
		{"no to", func(c map[string]string) { delete(c, apitypes.ConfigTo) }},
		{"cloudflare without account id", func(c map[string]string) {
			c[apitypes.ConfigProvider] = "cloudflare"
		}},
	}
	for _, tc := range cases {
		cfg := base()
		tc.mutate(cfg)
		if _, err := NewEmailHTTP(cfg); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}

	// A valid config must still be accepted, so the table above cannot pass by
	// rejecting everything.
	if _, err := NewEmailHTTP(base()); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	// Cloudflare is valid once the account id is present.
	cf := base()
	cf[apitypes.ConfigProvider] = "cloudflare"
	cf[apitypes.ConfigAccountID] = "acct123"
	if _, err := NewEmailHTTP(cf); err != nil {
		t.Fatalf("cloudflare config rejected: %v", err)
	}
}

func TestNewEmailHTTPDefaultsAndNormalisation(t *testing.T) {
	cfg := map[string]string{
		apitypes.ConfigProvider: "  RESEND  ", // case and space tolerant
		apitypes.ConfigAPIKey:   " k ",
		apitypes.ConfigFrom:     "a@b.c",
		apitypes.ConfigTo:       "x@y.z, q@r.s",
	}
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}
	if channel.Provider != ProviderResend {
		t.Errorf("provider = %q, want %q", channel.Provider, ProviderResend)
	}
	if channel.URL != "https://api.resend.com" {
		t.Errorf("url = %q, want the Resend default", channel.URL)
	}
	if len(channel.To) != 2 {
		t.Errorf("to = %v, want two recipients", channel.To)
	}
	if channel.APIKey != "k" {
		t.Errorf("api key = %q, want it trimmed", channel.APIKey)
	}
}

func TestEmailHTTPSendPerProvider(t *testing.T) {
	// Each case asserts the request that provider actually requires.
	cases := []struct {
		provider     string
		wantPath     string
		authHeader   string
		wantAuth     string
		checkBody    func(t *testing.T, body map[string]any)
		accountID    string
		responseBody string
	}{
		{
			provider:     "resend",
			wantPath:     "/emails",
			authHeader:   "Authorization",
			wantAuth:     "Bearer key_secret",
			responseBody: `{"id":"abc"}`,
			checkBody: func(t *testing.T, body map[string]any) {
				if body["subject"] != "alert" {
					t.Errorf("subject = %v", body["subject"])
				}
				to, ok := body["to"].([]any)
				if !ok || len(to) != 1 || to[0] != "ops@example.com" {
					t.Errorf("to = %v, want an array of addresses", body["to"])
				}
				if body["html"] == nil || body["text"] == nil {
					t.Error("both html and text should be present")
				}
			},
		},
		{
			provider:     "postmark",
			wantPath:     "/email",
			authHeader:   "X-Postmark-Server-Token",
			wantAuth:     "key_secret",
			responseBody: `{"ErrorCode":0,"Message":"OK"}`,
			checkBody: func(t *testing.T, body map[string]any) {
				// Postmark uses capitalised keys and a comma-joined To.
				if body["To"] != "ops@example.com" {
					t.Errorf("To = %v, want a comma-joined string", body["To"])
				}
				if body["HtmlBody"] == nil || body["TextBody"] == nil {
					t.Error("HtmlBody and TextBody should both be present")
				}
				if body["MessageStream"] != "outbound" {
					t.Errorf("MessageStream = %v", body["MessageStream"])
				}
			},
		},
		{
			provider:     "sendgrid",
			wantPath:     "/v3/mail/send",
			authHeader:   "Authorization",
			wantAuth:     "Bearer key_secret",
			responseBody: ``,
			checkBody: func(t *testing.T, body map[string]any) {
				pers, ok := body["personalizations"].([]any)
				if !ok || len(pers) != 1 {
					t.Fatalf("personalizations = %v", body["personalizations"])
				}
				first := pers[0].(map[string]any)
				to := first["to"].([]any)
				entry := to[0].(map[string]any)
				if entry["email"] != "ops@example.com" {
					t.Errorf("to[0].email = %v, want an {email} object", entry["email"])
				}
				content := body["content"].([]any)
				if len(content) != 2 {
					t.Errorf("content = %v, want text and html parts", content)
				}
			},
		},
		{
			provider:     "cloudflare",
			accountID:    "acct123",
			wantPath:     "/accounts/acct123/email/sending/send",
			authHeader:   "Authorization",
			wantAuth:     "Bearer key_secret",
			responseBody: `{"success":true,"errors":[],"result":{"delivered":["ops@example.com"]}}`,
			checkBody: func(t *testing.T, body map[string]any) {
				if body["from"] == nil || body["to"] == nil || body["subject"] == nil {
					t.Errorf("missing required fields: %v", body)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			server, captured := newCaptureServer(t, http.StatusOK, tc.responseBody)

			cfg := httpConfig(tc.provider, server.URL)
			if tc.accountID != "" {
				cfg[apitypes.ConfigAccountID] = tc.accountID
			}
			channel, err := NewEmailHTTP(cfg)
			if err != nil {
				t.Fatalf("NewEmailHTTP: %v", err)
			}

			err = channel.Send(context.Background(), Message{
				Title: "alert",
				Body:  "something changed",
			})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}

			if captured.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", captured.Method)
			}
			if captured.Path != tc.wantPath {
				t.Errorf("path = %s, want %s", captured.Path, tc.wantPath)
			}
			if got := captured.Header.Get(tc.authHeader); got != tc.wantAuth {
				t.Errorf("%s = %q, want %q", tc.authHeader, got, tc.wantAuth)
			}
			if ct := captured.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}

			var body map[string]any
			if err := json.Unmarshal(captured.Body, &body); err != nil {
				t.Fatalf("request body is not JSON: %v (%s)", err, captured.Body)
			}
			tc.checkBody(t, body)
		})
	}
}

func TestEmailHTTPSendIncludesMessageURL(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, `{}`)
	cfg := httpConfig("resend", server.URL)
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}

	if err := channel.Send(context.Background(), Message{
		Title: "drift", Body: "nginx moved", URL: "https://example.com/w/1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(captured.Body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	text, _ := body["text"].(string)
	if !strings.Contains(text, "https://example.com/w/1") {
		t.Errorf("text part lacks the URL: %q", text)
	}
	html, _ := body["html"].(string)
	if !strings.Contains(html, "https://example.com/w/1") {
		t.Errorf("html part lacks the URL: %q", html)
	}
}

func TestEmailHTTPSendUsesTitleOrFallbackSubject(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, `{}`)
	cfg := httpConfig("resend", server.URL)
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}

	if err := channel.Send(context.Background(), Message{Body: "no title here"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(captured.Body, &body)
	if body["subject"] != "dit notification" {
		t.Errorf("subject = %v, want the fallback", body["subject"])
	}
}

func TestEmailHTTPSendErrorsOnNon2xx(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusUnauthorized,
		`{"error":"invalid api key"}`)
	cfg := httpConfig("resend", server.URL)
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}

	err = channel.Send(context.Background(), Message{Title: "x", Body: "y"})
	if err == nil {
		t.Fatal("expected an error for a 401")
	}
	// The provider's own words must survive into the error, so an operator can
	// tell a bad key from a bad recipient.
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("error lost the provider detail: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error lacks the status: %v", err)
	}
}

// A provider can answer 200 and still report a failure in the body. Those must
// not be recorded as delivered.
func TestEmailHTTPSendDetectsFailureInSuccessBody(t *testing.T) {
	cases := []struct {
		name         string
		provider     string
		responseBody string
	}{
		{"cloudflare success false", "cloudflare", `{"success":false,"errors":[{"code":1001,"message":"domain not onboarded"}]}`},
		{"postmark non-zero error code", "postmark", `{"ErrorCode":406,"Message":"invalid sender"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newCaptureServer(t, http.StatusOK, tc.responseBody)
			cfg := httpConfig(tc.provider, server.URL)
			if tc.provider == "cloudflare" {
				cfg[apitypes.ConfigAccountID] = "acct"
			}
			channel, err := NewEmailHTTP(cfg)
			if err != nil {
				t.Fatalf("NewEmailHTTP: %v", err)
			}
			err = channel.Send(context.Background(), Message{Title: "x", Body: "y"})
			if err == nil {
				t.Fatal("a failure reported inside a 200 body must be an error")
			}
		})
	}
}

func TestEmailHTTPSendBuildsCloudflareURLFromAccount(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, `{"success":true}`)
	cfg := httpConfig("cloudflare", server.URL)
	cfg[apitypes.ConfigAccountID] = "acc-9"
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}
	if err := channel.Send(context.Background(), Message{Title: "x", Body: "y"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if captured.Path != "/accounts/acc-9/email/sending/send" {
		t.Errorf("path = %s", captured.Path)
	}
}

func TestEmailHTTPFromNameRendering(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, `{}`)
	cfg := httpConfig("resend", server.URL)
	cfg[apitypes.ConfigFromName] = "dit alerts"
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}
	if err := channel.Send(context.Background(), Message{Title: "x", Body: "y"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(captured.Body, &body)
	from, _ := body["from"].(string)
	if !strings.Contains(from, "dit alerts") || !strings.Contains(from, "dit@example.com") {
		t.Errorf("from = %q, want a named address", from)
	}
}

func TestEmailHTTPType(t *testing.T) {
	cfg := httpConfig("resend", "https://api.resend.com")
	channel, err := NewEmailHTTP(cfg)
	if err != nil {
		t.Fatalf("NewEmailHTTP: %v", err)
	}
	if channel.Type() != string(apitypes.ChannelEmailHTTP) {
		t.Errorf("type = %q, want %q", channel.Type(), apitypes.ChannelEmailHTTP)
	}
}

// The builder is the single mapping from stored type to transport, so a new
// type must be reachable through it.
func TestBuildDispatchesEmailHTTP(t *testing.T) {
	cfg := httpConfig("resend", "https://api.resend.com")
	channel, err := Build(string(apitypes.ChannelEmailHTTP), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := channel.(*EmailHTTPChannel); !ok {
		t.Fatalf("Build returned %T, want *EmailHTTPChannel", channel)
	}
}

func TestEmailProvidersListsAllSupported(t *testing.T) {
	names := ProviderNames()
	want := map[string]bool{"cloudflare": false, "resend": false, "postmark": false, "sendgrid": false}
	for _, n := range names {
		if _, ok := want[n]; !ok {
			t.Errorf("unexpected provider %q", n)
		}
		want[n] = true
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("provider %q is missing from the supported list", n)
		}
	}
}
