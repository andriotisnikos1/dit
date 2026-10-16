package server

// Probe for the whitespace handling of the watch scope in a query string. The
// event listing trims it; the flag listing did not, so a watch ID pasted with a
// stray space resolved for one endpoint and not the other.

import (
	"net/http"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestAdversarialFlagScopeWatchIDIsNotTrimmed records the difference between
// the two endpoints that accept a watch ID in a query string.
func TestAdversarialFlagScopeWatchIDIsNotTrimmed(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	padded := "%20" + id + "%20"
	rec := h.authed(http.MethodGet, "/api/v1/events?watch="+padded, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("events listing with a padded watch id = %d, want 200", rec.Code)
	}
	rec = h.authed(http.MethodGet, "/api/v1/flags?watch="+padded, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("flag listing with a padded watch id = %d, want 200 (the event listing trims it)", rec.Code)
	}

	// A whitespace-only scope is the global listing, not a watch lookup.
	rec = h.authed(http.MethodGet, "/api/v1/flags?watch=%20", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("flag listing with a blank watch scope = %d, want the global listing", rec.Code)
	}
}
