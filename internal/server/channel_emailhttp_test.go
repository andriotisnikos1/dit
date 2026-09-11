package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
)

// An email-http channel must be creatable through the API, keep its API key
// sealed and redacted, and be reportable as having a secret.
func TestCreateEmailHTTPChannelRedactsAPIKey(t *testing.T) {
	h := newHarness(t)

	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "cf-mail",
		Type: apitypes.ChannelEmailHTTP,
		Config: map[string]string{
			apitypes.ConfigProvider:  "cloudflare",
			apitypes.ConfigAccountID: "acct123",
			apitypes.ConfigAPIKey:    "cf_secret_token",
			apitypes.ConfigFrom:      "dit@services.andriotis.dev",
			apitypes.ConfigTo:        "nikos@andriotis.dev",
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var channel apitypes.Channel
	h.decode(rec, &channel)

	if channel.Type != apitypes.ChannelEmailHTTP {
		t.Errorf("type = %q", channel.Type)
	}
	if channel.Config[apitypes.ConfigAPIKey] != "" {
		t.Error("the create response leaked the provider API key")
	}
	if !channel.HasSecret {
		t.Error("HasSecret = false, want true for an email-http channel")
	}
	if channel.Config[apitypes.ConfigProvider] != "cloudflare" {
		t.Error("a non-secret field was dropped")
	}
	if channel.Config[apitypes.ConfigAccountID] != "acct123" {
		t.Error("account_id should survive, it is not a secret")
	}

	// The key must still be usable server-side.
	stored := h.storeChannel(channel.ID)
	if stored.Config[apitypes.ConfigAPIKey] != "cf_secret_token" {
		t.Error("the stored channel lost the API key")
	}
}

// The API must reject a config the transport cannot use, rather than storing a
// channel that fails on every notification.
func TestCreateEmailHTTPChannelValidatesConfig(t *testing.T) {
	h := newHarness(t)

	cases := []map[string]string{
		// unknown provider
		{apitypes.ConfigProvider: "nosuch", apitypes.ConfigAPIKey: "k",
			apitypes.ConfigFrom: "a@b.c", apitypes.ConfigTo: "d@e.f"},
		// cloudflare without the account id it needs
		{apitypes.ConfigProvider: "cloudflare", apitypes.ConfigAPIKey: "k",
			apitypes.ConfigFrom: "a@b.c", apitypes.ConfigTo: "d@e.f"},
		// no key
		{apitypes.ConfigProvider: "resend", apitypes.ConfigFrom: "a@b.c", apitypes.ConfigTo: "d@e.f"},
	}
	for i, cfg := range cases {
		rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
			Name:   "bad",
			Type:   apitypes.ChannelEmailHTTP,
			Config: cfg,
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("case %d: status = %d, want 422 (body %s)", i, rec.Code, rec.Body.String())
		}
	}
}

// Updating a channel without echoing the key back must not wipe it, the same
// guarantee the SMTP password has.
func TestUpdateEmailHTTPChannelPreservesAPIKey(t *testing.T) {
	h := newHarness(t)

	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "cf-mail",
		Type: apitypes.ChannelEmailHTTP,
		Config: map[string]string{
			apitypes.ConfigProvider: "resend",
			apitypes.ConfigAPIKey:   "original_key",
			apitypes.ConfigFrom:     "dit@example.com",
			apitypes.ConfigTo:       "ops@example.com",
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body %s", rec.Code, rec.Body.String())
	}
	var created apitypes.Channel
	h.decode(rec, &created)

	// Re-submit the redacted config, as a client that read it back would.
	updated := map[string]string{
		apitypes.ConfigProvider: "resend",
		apitypes.ConfigAPIKey:   "",
		apitypes.ConfigFrom:     "dit@example.com",
		apitypes.ConfigTo:       "new@example.com",
	}
	rec = h.authed(http.MethodPatch, "/api/v1/channels/"+created.ID,
		apitypes.UpdateChannelRequest{Config: &updated})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body %s", rec.Code, rec.Body.String())
	}

	stored := h.storeChannel(created.ID)
	if stored.Config[apitypes.ConfigAPIKey] != "original_key" {
		t.Errorf("api key = %q after an update that omitted it, want it preserved",
			stored.Config[apitypes.ConfigAPIKey])
	}
	if stored.Config[apitypes.ConfigTo] != "new@example.com" {
		t.Errorf("to = %q, want the update applied", stored.Config[apitypes.ConfigTo])
	}
}

// The type must be listed as valid, and the error for a bad type should name
// every option rather than a stale hardcoded pair.
func TestChannelTypesIncludeEmailHTTP(t *testing.T) {
	if !apitypes.ChannelEmailHTTP.Valid() {
		t.Error("email-http should be a valid channel type")
	}
	list := apitypes.ChannelTypeList()
	for _, want := range []string{"email", "email-http", "ntfy"} {
		if !strings.Contains(list, want) {
			t.Errorf("ChannelTypeList %q is missing %q", list, want)
		}
	}
}

// The server's own builder path must be able to construct the new channel.
func TestServerBuildsEmailHTTPChannel(t *testing.T) {
	channel, err := notify.Build(string(apitypes.ChannelEmailHTTP), map[string]string{
		apitypes.ConfigProvider: "resend",
		apitypes.ConfigAPIKey:   "k",
		apitypes.ConfigFrom:     "a@b.c",
		apitypes.ConfigTo:       "d@e.f",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if channel.Type() != string(apitypes.ChannelEmailHTTP) {
		t.Errorf("type = %q", channel.Type())
	}
	var _ = context.Background()
}
