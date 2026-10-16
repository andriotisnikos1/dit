package server

import (
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/registry"
)

func getFlag(t *testing.T, list apitypes.FlagList, name string) apitypes.Flag {
	t.Helper()
	for _, f := range list {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("flag %q missing from listing", name)
	return apitypes.Flag{}
}

func TestListGlobalFlags(t *testing.T) {
	h := newHarness(t)
	rec := h.authed("GET", "/api/v1/flags", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var list apitypes.FlagList
	h.decode(rec, &list)
	if len(list) != 5 {
		t.Fatalf("listed %d flags, want 5", len(list))
	}
	history := getFlag(t, list, "history")
	// JSON numbers decode into any as float64, so compare against float64(1).
	if history.Default != float64(1) || history.Global != nil {
		t.Errorf("history default=%v global=%v, want 1 / nil", history.Default, history.Global)
	}
	if !getFlag(t, list, "semver").PatternOnly {
		t.Error("semver not marked pattern_only")
	}
}

func TestSetAndUnsetGlobalFlag(t *testing.T) {
	h := newHarness(t)

	rec := h.authed("PUT", "/api/v1/flags/history", apitypes.SetFlagRequest{Value: 10})
	if rec.Code != 200 {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}
	var flag apitypes.Flag
	h.decode(rec, &flag)
	if flag.Global == nil || *flag.Global != float64(10) {
		t.Errorf("global = %v, want 10", flag.Global)
	}

	rec = h.authed("GET", "/api/v1/flags", nil)
	var list apitypes.FlagList
	h.decode(rec, &list)
	if v := getFlag(t, list, "notify_threshold").Effective; v != nil {
		t.Errorf("global listing exposes Effective = %v, want nil", v)
	}

	// Invalid values are 422.
	rec = h.authed("PUT", "/api/v1/flags/history", apitypes.SetFlagRequest{Value: 0})
	if rec.Code != 422 {
		t.Errorf("history=0 status = %d, want 422", rec.Code)
	}
	rec = h.authed("PUT", "/api/v1/flags/notify_threshold", apitypes.SetFlagRequest{Value: -3})
	if rec.Code != 422 {
		t.Errorf("notify_threshold=-3 status = %d, want 422", rec.Code)
	}
	// Missing value (JSON null/absent decodes to nil) is rejected: DELETE unsets.
	rec = h.authed("PUT", "/api/v1/flags/history", apitypes.SetFlagRequest{})
	if rec.Code != 422 {
		t.Errorf("empty value status = %d, want 422", rec.Code)
	}
	// Unknown flags are 404.
	rec = h.authed("PUT", "/api/v1/flags/nope", apitypes.SetFlagRequest{Value: 1})
	if rec.Code != 404 {
		t.Errorf("unknown flag status = %d, want 404", rec.Code)
	}

	rec = h.authed("DELETE", "/api/v1/flags/history", nil)
	if rec.Code != 204 {
		t.Fatalf("unset status = %d, want 204", rec.Code)
	}
	// DELETE is idempotent.
	rec = h.authed("DELETE", "/api/v1/flags/history", nil)
	if rec.Code != 204 {
		t.Errorf("second unset status = %d, want 204", rec.Code)
	}
}

func TestWatchFlagEndpoints(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	watch := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := watch.Watches[0].ID

	// The watch wire carries effective flags from the start.
	if watch.Watches[0].Flags["history"] != float64(1) {
		t.Errorf("watch flags = %v, want effective history present as 1", watch.Watches[0].Flags)
	}

	// semver on a tag watch is 422.
	rec := h.authed("PUT", "/api/v1/watches/"+id+"/flags/semver", apitypes.SetFlagRequest{Value: true})
	if rec.Code != 422 {
		t.Errorf("semver on a tag watch status = %d, want 422", rec.Code)
	}

	rec = h.authed("PUT", "/api/v1/watches/"+id+"/flags/history", apitypes.SetFlagRequest{Value: 5})
	if rec.Code != 200 {
		t.Fatalf("local set status = %d, want 200", rec.Code)
	}

	// Listing with watch scope shows local + effective.
	rec = h.authed("GET", "/api/v1/watches/"+id+"/flags", nil)
	var list apitypes.FlagList
	h.decode(rec, &list)
	history := getFlag(t, list, "history")
	if history.Local == nil || *history.Local != float64(5) {
		t.Errorf("local = %v, want 5", history.Local)
	}
	if history.Effective == nil || *history.Effective != float64(5) {
		t.Errorf("effective = %v, want 5", history.Effective)
	}
	semverFlag := getFlag(t, list, "semver")
	if semverFlag.Effective != nil {
		t.Errorf("semver effective on a tag watch = %v, want nil", semverFlag.Effective)
	}

	// The watch itself reports flags + flags_local.
	rec = h.authed("GET", "/api/v1/watches/"+id, nil)
	var w apitypes.Watch
	h.decode(rec, &w)
	if w.FlagsLocal["history"] == nil {
		t.Errorf("flags_local = %v, want history present", w.FlagsLocal)
	}
	if w.Flags["history"] == nil {
		t.Errorf("flags = %v, want effective history present", w.Flags)
	}

	rec = h.authed("DELETE", "/api/v1/watches/"+id+"/flags/history", nil)
	if rec.Code != 204 {
		t.Fatalf("local unset status = %d, want 204", rec.Code)
	}

	// Unknown watch is 404, not 500.
	rec = h.authed("PUT", "/api/v1/watches/w_missing/flags/history", apitypes.SetFlagRequest{Value: 5})
	if rec.Code != 404 {
		t.Errorf("unknown watch status = %d, want 404", rec.Code)
	}
}

func TestWatchFlagsPatternOnlyAcceptedForPattern(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	resp := h.createWatch("ghcr.io/owner/app", apitypes.CreateWatchRequest{Patterns: []string{"v*"}})
	id := resp.Watches[0].ID

	rec := h.authed("PUT", "/api/v1/watches/"+id+"/flags/semver", apitypes.SetFlagRequest{Value: true})
	if rec.Code != 200 {
		t.Fatalf("semver on a pattern watch status = %d, want 200", rec.Code)
	}
	rec = h.authed("GET", "/api/v1/flags?watch="+id, nil)
	var list apitypes.FlagList
	h.decode(rec, &list)
	if v := getFlag(t, list, "semver").Effective; v == nil || *v != true {
		t.Errorf("semver effective = %v, want true", v)
	}
}

func TestGlobalFlagInvalidValueMessageVerbatim(t *testing.T) {
	h := newHarness(t)
	rec := h.authed("PUT", "/api/v1/flags/history", apitypes.SetFlagRequest{Value: "abc"})
	if rec.Code != 422 {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	// The parser's own message is user-facing; it must survive to the client.
	if body := rec.Body.String(); !strings.Contains(body, "history expects an integer") {
		t.Errorf("body = %q, want the ParseValue message verbatim", body)
	}
}

func TestWatchFlagDeleteIdempotencyAnd404s(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	resp := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := resp.Watches[0].ID

	rec := h.authed("PUT", "/api/v1/watches/"+id+"/flags/history", apitypes.SetFlagRequest{Value: 5})
	if rec.Code != 200 {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}
	// Deleting an override is idempotent, set or not.
	for i := 1; i <= 2; i++ {
		if rec = h.authed("DELETE", "/api/v1/watches/"+id+"/flags/history", nil); rec.Code != 204 {
			t.Fatalf("delete #%d status = %d, want 204", i, rec.Code)
		}
	}
	if rec = h.authed("DELETE", "/api/v1/watches/w_missing/flags/history", nil); rec.Code != 404 {
		t.Errorf("unknown watch status = %d, want 404", rec.Code)
	}
	if rec = h.authed("DELETE", "/api/v1/watches/"+id+"/flags/nope", nil); rec.Code != 404 {
		t.Errorf("unknown flag status = %d, want 404", rec.Code)
	}
}

func TestFlagsWatchScopes404OnUnknownWatch(t *testing.T) {
	h := newHarness(t)
	rec := h.authed("GET", "/api/v1/flags?watch=w_missing", nil)
	if rec.Code != 404 {
		t.Errorf("GET /api/v1/flags?watch=unknown status = %d, want 404", rec.Code)
	}
	rec = h.authed("GET", "/api/v1/watches/w_missing/flags", nil)
	if rec.Code != 404 {
		t.Errorf("GET /api/v1/watches/unknown/flags status = %d, want 404", rec.Code)
	}
}

func TestWatchFlagInvalidValuesAre422(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	resp := h.createWatch("ghcr.io/owner/app", apitypes.CreateWatchRequest{Patterns: []string{"v*"}})
	id := resp.Watches[0].ID

	rec := h.authed("PUT", "/api/v1/watches/"+id+"/flags/history", apitypes.SetFlagRequest{})
	if rec.Code != 422 {
		t.Errorf("null value status = %d, want 422", rec.Code)
	}
	rec = h.authed("PUT", "/api/v1/watches/"+id+"/flags/semver_notify_threshold", apitypes.SetFlagRequest{Value: 0})
	if rec.Code != 422 {
		t.Errorf("zero threshold status = %d, want 422", rec.Code)
	}
	rec = h.authed("PUT", "/api/v1/watches/"+id+"/flags/semver_notify_threshold", apitypes.SetFlagRequest{Value: "nope"})
	if rec.Code != 422 {
		t.Errorf("non-numeric value status = %d, want 422", rec.Code)
	}
}

func TestFlagRoutesRequireAuth(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/flags"},
		{"PUT", "/api/v1/flags/history"},
		{"DELETE", "/api/v1/flags/history"},
		{"GET", "/api/v1/watches/w_x/flags"},
		{"PUT", "/api/v1/watches/w_x/flags/history"},
		{"DELETE", "/api/v1/watches/w_x/flags/history"},
	} {
		rec := h.do(tc.method, tc.path, nil, "\x00none")
		if rec.Code != 401 {
			t.Errorf("%s %s unauthenticated status = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}
