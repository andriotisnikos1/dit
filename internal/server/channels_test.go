package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

func testutilDigest(seed byte) string { return testutil.Digest(seed) }

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// storeChannel is a shortcut for reading a channel straight from the store, to
// assert on what was persisted rather than on what the API returned.
func (h *harness) storeChannel(id string) *store.ChannelRecord {
	h.t.Helper()
	channel, err := h.store.GetChannel(context.Background(), id)
	if err != nil {
		h.t.Fatalf("GetChannel(%s): %v", id, err)
	}
	return channel
}

func TestCreateChannelRedactsSecrets(t *testing.T) {
	h := newHarness(t)

	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "mail",
		Type: apitypes.ChannelEmail,
		Config: map[string]string{
			apitypes.ConfigSMTPHost: "smtp.example.com",
			apitypes.ConfigFrom:     "dit@example.com",
			apitypes.ConfigTo:       "ops@example.com",
			apitypes.ConfigUsername: "dit",
			apitypes.ConfigPassword: "super-secret",
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var channel apitypes.Channel
	h.decode(rec, &channel)

	if channel.Config[apitypes.ConfigPassword] != "" {
		t.Error("the create response leaked the SMTP password")
	}
	if !channel.HasSecret {
		t.Error("HasSecret = false, want true")
	}
	if channel.Config[apitypes.ConfigSMTPHost] != "smtp.example.com" {
		t.Error("the create response dropped a non-secret field")
	}
	if !channel.Enabled {
		t.Error("a new channel should be enabled")
	}

	// The secret must still be retrievable server-side.
	stored := h.storeChannel(channel.ID)
	if stored.Config[apitypes.ConfigPassword] != "super-secret" {
		t.Error("the stored channel lost the password")
	}
}

func TestGetAndListChannelsRedactSecrets(t *testing.T) {
	h := newHarness(t)

	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ntfy", Type: apitypes.ChannelNtfy,
		Config: map[string]string{
			apitypes.ConfigTopic: "alerts",
			apitypes.ConfigToken: "tk_secret",
		},
	})
	var created apitypes.Channel
	h.decode(rec, &created)

	rec = h.authed(http.MethodGet, "/api/v1/channels/"+created.ID, nil)
	var fetched apitypes.Channel
	h.decode(rec, &fetched)
	if fetched.Config[apitypes.ConfigToken] != "" {
		t.Error("GET channel leaked the ntfy token")
	}
	if !fetched.HasSecret {
		t.Error("HasSecret = false on a channel with a token")
	}

	rec = h.authed(http.MethodGet, "/api/v1/channels", nil)
	var list apitypes.List[apitypes.Channel]
	h.decode(rec, &list)
	for _, c := range list.Items {
		if c.Config[apitypes.ConfigToken] != "" || c.Config[apitypes.ConfigPassword] != "" {
			t.Errorf("LIST channels leaked a secret for %s", c.ID)
		}
	}
}

func TestUpdateChannelKeepsOmittedSecret(t *testing.T) {
	h := newHarness(t)

	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ntfy", Type: apitypes.ChannelNtfy,
		Config: map[string]string{
			apitypes.ConfigTopic: "alerts",
			apitypes.ConfigToken: "tk_secret",
		},
	})
	var created apitypes.Channel
	h.decode(rec, &created)

	// Echoing the redacted config back must not wipe the token.
	rec = h.authed(http.MethodPatch, "/api/v1/channels/"+created.ID, apitypes.UpdateChannelRequest{
		Config: &map[string]string{
			apitypes.ConfigTopic: "alerts-renamed",
			apitypes.ConfigToken: "",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	stored := h.storeChannel(created.ID)
	if stored.Config[apitypes.ConfigToken] != "tk_secret" {
		t.Errorf("token = %q after an update that omitted it, want the stored value",
			stored.Config[apitypes.ConfigToken])
	}
	if stored.Config[apitypes.ConfigTopic] != "alerts-renamed" {
		t.Errorf("topic = %q, want alerts-renamed", stored.Config[apitypes.ConfigTopic])
	}
}

func TestCreateChannelValidation(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name string
		req  apitypes.CreateChannelRequest
		want int
	}{
		{
			name: "unknown type",
			req:  apitypes.CreateChannelRequest{Name: "x", Type: "pigeon", Config: map[string]string{}},
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "missing name",
			req:  apitypes.CreateChannelRequest{Type: apitypes.ChannelNtfy, Config: map[string]string{apitypes.ConfigTopic: "t"}},
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "ntfy without a topic",
			req:  apitypes.CreateChannelRequest{Name: "x", Type: apitypes.ChannelNtfy, Config: map[string]string{}},
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "email without a host",
			req: apitypes.CreateChannelRequest{Name: "x", Type: apitypes.ChannelEmail,
				Config: map[string]string{apitypes.ConfigFrom: "a@b.c", apitypes.ConfigTo: "d@e.f"}},
			want: http.StatusUnprocessableEntity,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.authed(http.MethodPost, "/api/v1/channels", tc.req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestCreateChannelDuplicateNameConflicts(t *testing.T) {
	h := newHarness(t)
	body := apitypes.CreateChannelRequest{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	}
	rec := h.authed(http.MethodPost, "/api/v1/channels", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: status %d", rec.Code)
	}
	rec = h.authed(http.MethodPost, "/api/v1/channels", body)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate create: status %d, want 409", rec.Code)
	}
}

func TestChannelEnableDisableAndDefault(t *testing.T) {
	h := newHarness(t)
	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	})
	var channel apitypes.Channel
	h.decode(rec, &channel)

	rec = h.authed(http.MethodPatch, "/api/v1/channels/"+channel.ID, apitypes.UpdateChannelRequest{
		Enabled: boolPtr(false),
	})
	var updated apitypes.Channel
	h.decode(rec, &updated)
	if updated.Enabled {
		t.Error("Enabled = true after disabling")
	}

	rec = h.authed(http.MethodPatch, "/api/v1/channels/"+channel.ID, apitypes.UpdateChannelRequest{
		IsDefault: boolPtr(true),
	})
	h.decode(rec, &updated)
	if !updated.IsDefault {
		t.Error("IsDefault = false after marking default")
	}

	rec = h.authed(http.MethodPatch, "/api/v1/channels/"+channel.ID, apitypes.UpdateChannelRequest{
		IsDefault: boolPtr(false),
	})
	h.decode(rec, &updated)
	if updated.IsDefault {
		t.Error("IsDefault = true after unmarking default")
	}
}

func TestDeleteChannel(t *testing.T) {
	h := newHarness(t)
	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	})
	var channel apitypes.Channel
	h.decode(rec, &channel)

	rec = h.authed(http.MethodDelete, "/api/v1/channels/"+channel.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	rec = h.authed(http.MethodGet, "/api/v1/channels/"+channel.ID, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete: status %d, want 404", rec.Code)
	}
}

func TestTestChannelEndpoint(t *testing.T) {
	h := newHarness(t)
	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	})
	var channel apitypes.Channel
	h.decode(rec, &channel)
	recording := h.notifier.Set(channel.ID, string(apitypes.ChannelNtfy))

	rec = h.authed(http.MethodPost, "/api/v1/channels/"+channel.ID+"/test", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var result apitypes.ChannelTestResult
	h.decode(rec, &result)
	if !result.OK {
		t.Errorf("OK = false, error %q", result.Error)
	}
	if recording.Count() != 1 {
		t.Errorf("the test sent %d messages, want 1", recording.Count())
	}

	// A failing transport is reported in the body with a 200: the API call
	// succeeded, the delivery did not.
	recording.FailFor = func(notify.Message) bool { return true }
	rec = h.authed(http.MethodPost, "/api/v1/channels/"+channel.ID+"/test", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d for a failing delivery, want 200", rec.Code)
	}

	// Unknown channel.
	rec = h.authed(http.MethodPost, "/api/v1/channels/c_missing/test", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("test on an unknown channel: status %d, want 404", rec.Code)
	}
}

func TestChannelListFilterByType(t *testing.T) {
	h := newHarness(t)
	h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ntfy-one", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	})
	h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "mail-one", Type: apitypes.ChannelEmail,
		Config: map[string]string{
			apitypes.ConfigSMTPHost: "h", apitypes.ConfigFrom: "a@b.c", apitypes.ConfigTo: "d@e.f",
		},
	})

	rec := h.authed(http.MethodGet, "/api/v1/channels?type=ntfy", nil)
	var list apitypes.List[apitypes.Channel]
	h.decode(rec, &list)
	if list.Total != 1 || list.Items[0].Name != "ntfy-one" {
		t.Errorf("type filter returned %+v, want only ntfy-one", list.Items)
	}

	rec = h.authed(http.MethodGet, "/api/v1/channels?type=bogus", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d for an unknown type, want 422", rec.Code)
	}
}
