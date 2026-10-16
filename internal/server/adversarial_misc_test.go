package server

// Remaining adversarial probes for the flag surface: exactly what a wrong method
// on a flag route returns, and that deleting one override leaves its siblings
// intact.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestAdversarialMethodMismatchShape records exactly what a wrong method on a
// flag route returns. The router's catch-all matches every method, so a wrong
// method on a real route is reported as an unknown endpoint: 404 in the standard
// envelope, never a 2xx and never a 5xx.
func TestAdversarialMethodMismatchShape(t *testing.T) {
	h := newHarness(t)

	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/flags/history"},
		{http.MethodPatch, "/api/v1/flags/history"},
		{http.MethodGet, "/api/v1/flags/history"},
		{http.MethodPut, "/api/v1/flags"},
		{http.MethodDelete, "/api/v1/flags"},
		{http.MethodPost, "/api/v1/watches/w_x/flags/history"},
		{http.MethodPatch, "/api/v1/watches/w_x/flags/history"},
		{http.MethodPut, "/api/v1/watches/w_x/flags"},
	}
	for _, c := range cases {
		rec := h.authed(c.method, c.path, apitypes.SetFlagRequest{Value: 2})
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want the 404 unknown-endpoint answer", c.method, c.path, rec.Code)
			continue
		}
		if code := h.errorCode(rec); code != apitypes.CodeNotFound {
			t.Errorf("%s %s envelope code = %q, want %q", c.method, c.path, code, apitypes.CodeNotFound)
		}
		if body := rec.Body.String(); !strings.Contains(body, "no such endpoint") {
			t.Errorf("%s %s body = %q, want the unknown-endpoint message", c.method, c.path, body)
		}
	}
}

// TestAdversarialDeletingOneOverrideKeepsSiblings pins that unsetting one flag
// leaves the other overrides and falls back to the default or global value.
func TestAdversarialDeletingOneOverrideKeepsSiblings(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	if rec := h.authed(http.MethodPut, "/api/v1/flags/notify_threshold", apitypes.SetFlagRequest{Value: 6}); rec.Code != 200 {
		t.Fatalf("global set = %d", rec.Code)
	}
	for _, set := range []struct {
		name  string
		value any
	}{{"history", 4}, {"notify_threshold", 2}} {
		if rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/"+set.name,
			apitypes.SetFlagRequest{Value: set.value}); rec.Code != 200 {
			t.Fatalf("local set %s = %d", set.name, rec.Code)
		}
	}

	if rec := h.authed(http.MethodDelete, "/api/v1/watches/"+id+"/flags/history", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}

	list := h.flagsOf(id)
	history := getFlag(t, list, "history")
	if history.Local != nil {
		t.Errorf("deleted override still local: %v", *history.Local)
	}
	// history has no global value, so it falls back to the default 1.
	if history.Effective == nil || *history.Effective != float64(1) {
		t.Errorf("history effective after the delete = %v, want the default 1", history.Effective)
	}
	sibling := getFlag(t, list, "notify_threshold")
	if sibling.Local == nil || *sibling.Local != float64(2) {
		t.Errorf("sibling override = %v, want 2", sibling.Local)
	}
	if sibling.Effective == nil || *sibling.Effective != float64(2) {
		t.Errorf("sibling effective = %v, want 2 (the local value)", sibling.Effective)
	}
}
