package server

import (
	"net/http"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestChannelEndpointsAcceptNames checks the documented name-based CLI usage:
// `dit channel test ops`, `dit channel default ops`, `dit channel disable ops`
// all address a channel by name, so the API must resolve names as well as IDs.
func TestChannelEndpointsAcceptNames(t *testing.T) {
	h := newHarness(t)

	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "alerts"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create channel: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created apitypes.Channel
	h.decode(rec, &created)

	// GET by name.
	rec = h.authed(http.MethodGet, "/api/v1/channels/ops", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET by name: status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var fetched apitypes.Channel
	h.decode(rec, &fetched)
	if fetched.ID != created.ID {
		t.Errorf("GET by name returned %s, want %s", fetched.ID, created.ID)
	}

	// GET by ID still works.
	rec = h.authed(http.MethodGet, "/api/v1/channels/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("GET by ID: status %d, want 200", rec.Code)
	}

	// PATCH by name.
	rec = h.authed(http.MethodPatch, "/api/v1/channels/ops", apitypes.UpdateChannelRequest{
		IsDefault: boolPtr(true),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH by name: status %d, body %s", rec.Code, rec.Body.String())
	}
	var updated apitypes.Channel
	h.decode(rec, &updated)
	if !updated.IsDefault {
		t.Error("PATCH by name did not apply the change")
	}
	if updated.ID != created.ID {
		t.Errorf("PATCH by name hit %s, want %s", updated.ID, created.ID)
	}

	// POST /test by name.
	recording := h.notifier.Set(created.ID, string(apitypes.ChannelNtfy))
	rec = h.authed(http.MethodPost, "/api/v1/channels/ops/test", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("test by name: status %d, body %s", rec.Code, rec.Body.String())
	}
	var result apitypes.ChannelTestResult
	h.decode(rec, &result)
	if !result.OK || result.ChannelID != created.ID {
		t.Errorf("test by name returned %+v, want OK for %s", result, created.ID)
	}
	if recording.Count() != 1 {
		t.Errorf("test by name sent %d messages, want 1", recording.Count())
	}

	// DELETE by name.
	rec = h.authed(http.MethodDelete, "/api/v1/channels/ops", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE by name: status %d, want 204", rec.Code)
	}
	rec = h.authed(http.MethodGet, "/api/v1/channels/"+created.ID, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete by name: status %d, want 404", rec.Code)
	}
}

// TestChannelLookupUnknownNameIs404 checks that a missing name is a clean 404
// rather than a server error.
func TestChannelLookupUnknownNameIs404(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{
		"/api/v1/channels/does-not-exist",
		"/api/v1/channels/c_missingid",
	} {
		rec := h.authed(http.MethodGet, path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, rec.Code)
		}
	}

	rec := h.authed(http.MethodPost, "/api/v1/channels/nope/test", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("test with an unknown name: status %d, want 404", rec.Code)
	}

	rec = h.authed(http.MethodPatch, "/api/v1/channels/nope", apitypes.UpdateChannelRequest{
		IsDefault: boolPtr(true),
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH with an unknown name: status %d, want 404", rec.Code)
	}

	rec = h.authed(http.MethodDelete, "/api/v1/channels/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE with an unknown name: status %d, want 404", rec.Code)
	}
}
