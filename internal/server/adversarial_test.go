package server

// Adversarial verification pass for the flag API surface and its end-to-end
// wiring into the check engine. Every test here records a falsifiable claim
// about the flag routes, the flag views and the real engine behaviour observed
// through HTTP.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
)

// ---------- helpers ----------

// channelWithRecorder creates a channel and returns the recording transport the
// engine will use for it.
func (h *harness) channelWithRecorder(name string) (apitypes.Channel, *notify.Recording) {
	h.t.Helper()
	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name:   name,
		Type:   apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		h.t.Fatalf("create channel: status %d, body %s", rec.Code, rec.Body.String())
	}
	var ch apitypes.Channel
	h.decode(rec, &ch)
	return ch, h.notifier.Set(ch.ID, string(apitypes.ChannelNtfy))
}

// check runs a real check through the API and returns the decoded result.
func (h *harness) check(id string) apitypes.CheckResult {
	h.t.Helper()
	rec := h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("check %s: status %d, body %s", id, rec.Code, rec.Body.String())
	}
	var result apitypes.CheckResult
	h.decode(rec, &result)
	return result
}

// watchEvents lists a watch's events through the API.
func (h *harness) watchEvents(id string) apitypes.List[apitypes.Event] {
	h.t.Helper()
	rec := h.authed(http.MethodGet, "/api/v1/events?watch="+id, nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("list events: status %d, body %s", rec.Code, rec.Body.String())
	}
	var list apitypes.List[apitypes.Event]
	h.decode(rec, &list)
	return list
}

// flagsOf lists flags for a scope ("" = global).
func (h *harness) flagsOf(watchID string) apitypes.FlagList {
	h.t.Helper()
	path := "/api/v1/flags"
	if watchID != "" {
		path = "/api/v1/watches/" + watchID + "/flags"
	}
	rec := h.authed(http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("list flags %s: status %d, body %s", path, rec.Code, rec.Body.String())
	}
	var list apitypes.FlagList
	h.decode(rec, &list)
	return list
}

// ---------- 1. the six flag routes ----------

// TestAdversarialFlagRoutesAuthAndMethod covers auth (401), method handling and
// the error envelope on all six flag routes.
func TestAdversarialFlagRoutesAuthAndMethod(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	routes := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/flags"},
		{http.MethodPut, "/api/v1/flags/history"},
		{http.MethodDelete, "/api/v1/flags/history"},
		{http.MethodGet, "/api/v1/watches/" + id + "/flags"},
		{http.MethodPut, "/api/v1/watches/" + id + "/flags/history"},
		{http.MethodDelete, "/api/v1/watches/" + id + "/flags/history"},
	}

	// Unauthenticated: 401 with the unauthorized envelope, for every route.
	for _, rt := range routes {
		rec := h.do(rt.method, rt.path, nil, "\x00none")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401", rt.method, rt.path, rec.Code)
			continue
		}
		if code := h.errorCode(rec); code != apitypes.CodeUnauthorized {
			t.Errorf("%s %s unauthenticated code = %q, want %q", rt.method, rt.path, code, apitypes.CodeUnauthorized)
		}
	}

	// A valid token on the wrong method must not be treated as success.
	mismatched := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/flags"},
		{http.MethodDelete, "/api/v1/flags"},
		{http.MethodPost, "/api/v1/flags/history"},
		{http.MethodPatch, "/api/v1/flags/history"},
		{http.MethodPost, "/api/v1/watches/" + id + "/flags/history"},
		{http.MethodPatch, "/api/v1/watches/" + id + "/flags/history"},
	}
	for _, rt := range mismatched {
		rec := h.authed(rt.method, rt.path, apitypes.SetFlagRequest{Value: 2})
		if rec.Code >= 200 && rec.Code < 300 {
			t.Errorf("%s %s = %d, want an error status", rt.method, rt.path, rec.Code)
		}
		if rec.Code < 400 || rec.Code >= 500 {
			t.Errorf("%s %s = %d, want a 4xx", rt.method, rt.path, rec.Code)
		}
	}
}

// TestAdversarialUnknownFlagAndWatchAre404 pins the 404s and their envelope.
func TestAdversarialUnknownFlagAndWatchAre404(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/api/v1/flags/nope", apitypes.SetFlagRequest{Value: 1}},
		{http.MethodDelete, "/api/v1/flags/nope", nil},
		{http.MethodPut, "/api/v1/watches/" + id + "/flags/nope", apitypes.SetFlagRequest{Value: 1}},
		{http.MethodDelete, "/api/v1/watches/" + id + "/flags/nope", nil},
		{http.MethodGet, "/api/v1/flags?watch=w_missing", nil},
		{http.MethodGet, "/api/v1/watches/w_missing/flags", nil},
		{http.MethodPut, "/api/v1/watches/w_missing/flags/history", apitypes.SetFlagRequest{Value: 1}},
		{http.MethodDelete, "/api/v1/watches/w_missing/flags/history", nil},
	}
	for _, tc := range cases {
		rec := h.authed(tc.method, tc.path, tc.body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d want 404 (body %s)", tc.method, tc.path, rec.Code, rec.Body.String())
			continue
		}
		if code := h.errorCode(rec); code != apitypes.CodeNotFound {
			t.Errorf("%s %s code = %q, want %q", tc.method, tc.path, code, apitypes.CodeNotFound)
		}
	}
}

// TestAdversarial422Matrix covers the value-validation matrix on both scopes.
func TestAdversarial422Matrix(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	tag := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	pat := h.createWatch("ghcr.io/owner/v2", apitypes.CreateWatchRequest{Patterns: []string{"v*"}})
	tagID, patID := tag.Watches[0].ID, pat.Watches[0].ID

	type tc struct {
		name  string
		scope string // watch id, or "" for global
		flag  string
		body  any
	}
	cases := []tc{
		{"null value global", "", "history", apitypes.SetFlagRequest{}},
		{"null value watch", tagID, "history", apitypes.SetFlagRequest{}},
		{"non-numeric int global", "", "history", apitypes.SetFlagRequest{Value: "abc"}},
		{"non-numeric int watch", tagID, "history", apitypes.SetFlagRequest{Value: "abc"}},
		{"zero history global", "", "history", apitypes.SetFlagRequest{Value: 0}},
		{"zero threshold watch", patID, "semver_notify_threshold", apitypes.SetFlagRequest{Value: 0}},
		{"negative threshold global", "", "notify_threshold", apitypes.SetFlagRequest{Value: -3}},
		{"negative threshold watch", tagID, "notify_threshold", apitypes.SetFlagRequest{Value: -1}},
		{"float value global", "", "history", apitypes.SetFlagRequest{Value: 1.5}},
		{"pattern-only on tag watch", tagID, "semver", apitypes.SetFlagRequest{Value: true}},
		{"pattern-only stable on tag watch", tagID, "semver_stable", apitypes.SetFlagRequest{Value: true}},
		{"pattern-only threshold on tag watch", tagID, "semver_notify_threshold", apitypes.SetFlagRequest{Value: 2}},
		{"invalid bool string", patID, "semver", apitypes.SetFlagRequest{Value: "yes-please"}},
		{"bool from number", patID, "semver", apitypes.SetFlagRequest{Value: 2}},
		{"object value", "", "history", apitypes.SetFlagRequest{Value: map[string]any{"a": 1}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := "/api/v1/flags/" + c.flag
			if c.scope != "" {
				path = "/api/v1/watches/" + c.scope + "/flags/" + c.flag
			}
			rec := h.authed(http.MethodPut, path, c.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("PUT %s = %d, want 422 (body %s)", path, rec.Code, rec.Body.String())
			}
			if code := h.errorCode(rec); code != apitypes.CodeValidationFailed {
				t.Errorf("code = %q, want %q", code, apitypes.CodeValidationFailed)
			}
		})
	}
}

// TestAdversarialRejectedRequestWritesNothing is bug class G: a rejected PUT
// must not have written anything, on either scope.
func TestAdversarialRejectedRequestWritesNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	// Rejected local write: pattern-only flag on a tag watch.
	if rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/semver",
		apitypes.SetFlagRequest{Value: true}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("semver on a tag watch = %d, want 422", rec.Code)
	}
	// Rejected local write: invalid value.
	if rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/history",
		apitypes.SetFlagRequest{Value: 0}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("history=0 on a watch = %d, want 422", rec.Code)
	}
	got, err := h.store.GetWatch(ctx, id)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if len(got.Flags) != 0 {
		t.Errorf("rejected writes left overrides behind: %v", got.Flags)
	}

	// Rejected global write: invalid value.
	if rec := h.authed(http.MethodPut, "/api/v1/flags/history",
		apitypes.SetFlagRequest{Value: 0}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("global history=0 = %d, want 422", rec.Code)
	}
	if rec := h.authed(http.MethodPut, "/api/v1/flags/notify_threshold",
		apitypes.SetFlagRequest{Value: "junk"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("global notify_threshold=junk = %d, want 422", rec.Code)
	}
	settings, err := h.store.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if len(settings) != 0 {
		t.Errorf("rejected global writes left settings behind: %v", settings)
	}
}

// TestAdversarialDeleteIdempotencyAndState covers the idempotent DELETEs.
func TestAdversarialDeleteIdempotencyAndState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	// Unsetting a never-set global flag is 204 and writes nothing.
	for i := 1; i <= 2; i++ {
		if rec := h.authed(http.MethodDelete, "/api/v1/flags/history", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("global delete #%d = %d, want 204", i, rec.Code)
		}
	}
	// Unsetting a never-set watch override is 204 and writes nothing.
	for i := 1; i <= 2; i++ {
		if rec := h.authed(http.MethodDelete, "/api/v1/watches/"+id+"/flags/history", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("watch delete #%d = %d, want 204", i, rec.Code)
		}
	}
	got, err := h.store.GetWatch(ctx, id)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if len(got.Flags) != 0 {
		t.Errorf("idempotent deletes wrote state: %v", got.Flags)
	}

	// Set then unset really removes the value, on both scopes.
	if rec := h.authed(http.MethodPut, "/api/v1/flags/history", apitypes.SetFlagRequest{Value: 9}); rec.Code != 200 {
		t.Fatalf("global set = %d", rec.Code)
	}
	if rec := h.authed(http.MethodDelete, "/api/v1/flags/history", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("global unset = %d", rec.Code)
	}
	settings, _ := h.store.GetSettings(ctx)
	if _, ok := settings["history"]; ok {
		t.Errorf("global unset left %q behind", "history")
	}

	if rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/history", apitypes.SetFlagRequest{Value: 9}); rec.Code != 200 {
		t.Fatalf("watch set = %d", rec.Code)
	}
	if rec := h.authed(http.MethodDelete, "/api/v1/watches/"+id+"/flags/history", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("watch unset = %d", rec.Code)
	}
	if got, err = h.store.GetWatch(ctx, id); err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if _, ok := got.Flags["history"]; ok {
		t.Errorf("watch unset left the override behind: %v", got.Flags)
	}
}

// ---------- 2. setFlag: validation order and the returned view ----------

// TestAdversarialValidationOrderIsSideEffectFree pins the documented order:
// unknown -> null -> parse -> watch load -> kind gate -> write. Every earlier
// failure must win, and no write may happen.
func TestAdversarialValidationOrderIsSideEffectFree(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	// Unknown flag beats an unknown watch (404 with the flag message).
	rec := h.authed(http.MethodPut, "/api/v1/watches/w_missing/flags/nope", apitypes.SetFlagRequest{Value: 1})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown flag + unknown watch = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "unknown flag") {
		t.Errorf("body = %q, want the unknown-flag message to win", body)
	}

	// Parse failure beats an unknown watch (422, no store write, no 404).
	rec = h.authed(http.MethodPut, "/api/v1/watches/w_missing/flags/history", apitypes.SetFlagRequest{Value: 0})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad value + unknown watch = %d, want 422", rec.Code)
	}

	// A null value is rejected before the watch is even loaded.
	rec = h.authed(http.MethodPut, "/api/v1/watches/w_missing/flags/history", apitypes.SetFlagRequest{})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null value + unknown watch = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "value is required") {
		t.Errorf("null value body = %q, want the required-value message", rec.Body.String())
	}

	// The kind gate is checked before the write: nothing was persisted.
	rec = h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/semver", apitypes.SetFlagRequest{Value: true})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("semver on a tag watch = %d, want 422", rec.Code)
	}
	got, _ := h.store.GetWatch(ctx, id)
	if len(got.Flags) != 0 {
		t.Errorf("kind-gate rejection wrote state: %v", got.Flags)
	}
}

// TestAdversarialSetWatchFlagResponseMatchesGet is the key consistency claim:
// the Flag view returned by PUT must agree with a subsequent GET.
func TestAdversarialSetWatchFlagResponseMatchesGet(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/history", apitypes.SetFlagRequest{Value: 5})
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}
	var put apitypes.Flag
	h.decode(rec, &put)

	get := getFlag(t, h.flagsOf(id), "history")

	if put.Local == nil {
		t.Errorf("PUT response local = nil, want 5")
	} else if *put.Local != float64(5) {
		t.Errorf("PUT response local = %v, want 5", *put.Local)
	}
	if put.Effective == nil {
		t.Errorf("PUT response effective = nil, want 5")
	} else if *put.Effective != float64(5) {
		t.Errorf("PUT response effective = %v, want 5", *put.Effective)
	}
	// The view must not contradict a fresh read of the same scope.
	if put.Local != nil && get.Local != nil && *put.Local != *get.Local {
		t.Errorf("PUT local %v != GET local %v", *put.Local, *get.Local)
	}
	if put.Effective != nil && get.Effective != nil && *put.Effective != *get.Effective {
		t.Errorf("PUT effective %v != GET effective %v", *put.Effective, *get.Effective)
	}

	// A second, independent flag on the same watch: the returned view must
	// show the new override AND keep the previous one visible.
	rec = h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/notify_threshold", apitypes.SetFlagRequest{Value: 4})
	if rec.Code != http.StatusOK {
		t.Fatalf("second set status = %d, want 200", rec.Code)
	}
	var second apitypes.Flag
	h.decode(rec, &second)
	if second.Name != "notify_threshold" {
		t.Errorf("returned flag name = %q, want notify_threshold", second.Name)
	}
	if second.Local == nil || *second.Local != float64(4) {
		t.Errorf("second PUT local = %v, want 4", second.Local)
	}
	if second.Effective == nil || *second.Effective != float64(4) {
		t.Errorf("second PUT effective = %v, want 4", second.Effective)
	}
	if second.Global != nil {
		t.Errorf("second PUT global = %v, want nil", *second.Global)
	}
}

// TestAdversarialSetGlobalFlagResponseMatchesGet covers the global scope.
func TestAdversarialSetGlobalFlagResponseMatchesGet(t *testing.T) {
	h := newHarness(t)

	rec := h.authed(http.MethodPut, "/api/v1/flags/notify_threshold", apitypes.SetFlagRequest{Value: 3})
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}
	var put apitypes.Flag
	h.decode(rec, &put)
	if put.Global == nil || *put.Global != float64(3) {
		t.Errorf("PUT global = %v, want 3", put.Global)
	}
	if put.Effective != nil {
		t.Errorf("global scope PUT exposed effective = %v, want nil", *put.Effective)
	}
	if put.Local != nil {
		t.Errorf("global scope PUT exposed local = %v, want nil", *put.Local)
	}

	get := getFlag(t, h.flagsOf(""), "notify_threshold")
	if get.Global == nil || *get.Global != float64(3) {
		t.Errorf("GET global = %v, want 3", get.Global)
	}
	if get.Default != float64(1) {
		t.Errorf("GET default = %v, want 1", get.Default)
	}

	// Overwriting keeps exactly one value.
	if rec := h.authed(http.MethodPut, "/api/v1/flags/notify_threshold", apitypes.SetFlagRequest{Value: 7}); rec.Code != 200 {
		t.Fatalf("overwrite status = %d", rec.Code)
	}
	if got := getFlag(t, h.flagsOf(""), "notify_threshold"); *got.Global != float64(7) {
		t.Errorf("after overwrite global = %v, want 7", *got.Global)
	}
	settings, err := h.store.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings["notify_threshold"] != "7" {
		t.Errorf("stored settings = %v, want notify_threshold=7", settings)
	}
}

// TestAdversarialGlobalValueIsVisibleInWatchScope pins that a watch-scoped
// listing reports the global value and inherits it as effective.
func TestAdversarialGlobalValueIsVisibleInWatchScope(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	if rec := h.authed(http.MethodPut, "/api/v1/flags/notify_threshold", apitypes.SetFlagRequest{Value: 4}); rec.Code != 200 {
		t.Fatalf("global set = %d", rec.Code)
	}

	for _, list := range []apitypes.FlagList{h.flagsOf(""), h.flagsOf(id)} {
		f := getFlag(t, list, "notify_threshold")
		if f.Global == nil || *f.Global != float64(4) {
			t.Errorf("global = %v, want 4", f.Global)
		}
		if f.Local != nil {
			t.Errorf("local = %v, want nil (no override)", *f.Local)
		}
	}

	// Watch scope inherits the global value as effective; the global scope has
	// no effective value at all.
	scoped := getFlag(t, h.flagsOf(id), "notify_threshold")
	if scoped.Effective == nil || *scoped.Effective != float64(4) {
		t.Errorf("effective = %v, want 4 (inherited)", scoped.Effective)
	}
	if v := getFlag(t, h.flagsOf(""), "notify_threshold").Effective; v != nil {
		t.Errorf("global listing effective = %v, want nil", *v)
	}

	// An override then wins over the global value.
	if rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/notify_threshold",
		apitypes.SetFlagRequest{Value: 2}); rec.Code != 200 {
		t.Fatalf("local set = %d", rec.Code)
	}
	f := getFlag(t, h.flagsOf(id), "notify_threshold")
	if *f.Global != float64(4) || f.Local == nil || *f.Local != float64(2) || *f.Effective != float64(2) {
		t.Errorf("global/local/effective = %v/%v/%v, want 4/2/2", *f.Global, *f.Local, *f.Effective)
	}
	// And the global scope is untouched by the override.
	if g := getFlag(t, h.flagsOf(""), "notify_threshold"); *g.Global != float64(4) {
		t.Errorf("global changed by a local set: %v", *g.Global)
	}

	// The watch wire agrees.
	rec := h.authed(http.MethodGet, "/api/v1/watches/"+id, nil)
	var wire apitypes.Watch
	h.decode(rec, &wire)
	if wire.Flags["notify_threshold"] != float64(2) {
		t.Errorf("watch flags = %v, want notify_threshold 2", wire.Flags)
	}
	if wire.FlagsLocal["notify_threshold"] != float64(2) {
		t.Errorf("watch flags_local = %v, want notify_threshold 2", wire.FlagsLocal)
	}
}

// ---------- 3. buildFlagList ----------

// TestAdversarialFlagListShape checks pointer semantics for every flag in both
// scopes, plus multiple overrides on one watch.
func TestAdversarialFlagListShape(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	pat := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{Patterns: []string{"v*"}})
	patID := pat.Watches[0].ID

	// Global values for two flags, overrides for two others on the watch.
	for _, set := range []struct {
		path string
		body any
	}{
		{"/api/v1/flags/history", apitypes.SetFlagRequest{Value: 6}},
		{"/api/v1/flags/semver_stable", apitypes.SetFlagRequest{Value: true}},
		{"/api/v1/watches/" + patID + "/flags/notify_threshold", apitypes.SetFlagRequest{Value: 4}},
		{"/api/v1/watches/" + patID + "/flags/semver", apitypes.SetFlagRequest{Value: true}},
	} {
		if rec := h.authed(http.MethodPut, set.path, set.body); rec.Code != 200 {
			t.Fatalf("PUT %s = %d", set.path, rec.Code)
		}
	}

	global := h.flagsOf("")
	if len(global) != 5 {
		t.Fatalf("global listing has %d flags, want 5", len(global))
	}
	for _, f := range global {
		if f.Local != nil {
			t.Errorf("global listing %s has local = %v", f.Name, *f.Local)
		}
		if f.Effective != nil {
			t.Errorf("global listing %s has effective = %v", f.Name, *f.Effective)
		}
		if f.Default == nil {
			t.Errorf("global listing %s has no default", f.Name)
		}
	}
	if v := getFlag(t, global, "history").Global; v == nil || *v != float64(6) {
		t.Errorf("global history = %v, want 6", v)
	}
	if v := getFlag(t, global, "notify_threshold").Global; v != nil {
		t.Errorf("global notify_threshold = %v, want nil", *v)
	}

	scoped := h.flagsOf(patID)
	if len(scoped) != 5 {
		t.Fatalf("watch listing has %d flags, want 5", len(scoped))
	}
	want := map[string]struct {
		global, local, effective any // nil means "must be absent"
	}{
		"history":                 {float64(6), nil, float64(6)},
		"notify_threshold":        {nil, float64(4), float64(4)},
		"semver":                  {nil, true, true},
		"semver_stable":           {true, nil, true},
		"semver_notify_threshold": {nil, nil, float64(1)},
	}
	for name, exp := range want {
		f := getFlag(t, scoped, name)
		checkPtr(t, name+".global", f.Global, exp.global)
		checkPtr(t, name+".local", f.Local, exp.local)
		checkPtr(t, name+".effective", f.Effective, exp.effective)
	}
}

// TestAdversarialPatternOnlyEffectiveOmittedForTagWatches checks the kind gate
// on both the listing and the watch wire.
func TestAdversarialPatternOnlyEffectiveOmittedForTagWatches(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	tag := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	tagID := tag.Watches[0].ID
	pat := h.createWatch("ghcr.io/owner/v2", apitypes.CreateWatchRequest{Patterns: []string{"v*"}})
	patID := pat.Watches[0].ID

	for _, name := range []string{"semver", "semver_stable", "semver_notify_threshold"} {
		if v := getFlag(t, h.flagsOf(tagID), name).Effective; v != nil {
			t.Errorf("tag watch %s effective = %v, want nil", name, *v)
		}
		if v := getFlag(t, h.flagsOf(patID), name).Effective; v == nil {
			t.Errorf("pattern watch %s effective = nil, want a value", name)
		}
	}
	// history / notify_threshold are effective for both kinds.
	for _, id := range []string{tagID, patID} {
		for _, name := range []string{"history", "notify_threshold"} {
			if v := getFlag(t, h.flagsOf(id), name).Effective; v == nil {
				t.Errorf("watch %s %s effective = nil, want a value", id, name)
			}
		}
	}

	// The watch wire omits pattern-only keys for a tag watch.
	rec := h.authed(http.MethodGet, "/api/v1/watches/"+tagID, nil)
	var wire apitypes.Watch
	h.decode(rec, &wire)
	for _, name := range []string{"semver", "semver_stable", "semver_notify_threshold"} {
		if _, ok := wire.Flags[name]; ok {
			t.Errorf("tag watch wire carries %s = %v", name, wire.Flags[name])
		}
	}
	if _, ok := wire.Flags["history"]; !ok {
		t.Errorf("tag watch wire is missing history: %v", wire.Flags)
	}
}

// TestAdversarialCorruptStoredStateDegrades proves a poisoned settings row or
// an unknown stored flag name degrades to defaults instead of failing.
func TestAdversarialCorruptStoredStateDegrades(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	w := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := w.Watches[0].ID

	// A global value the parser rejects.
	if err := h.store.SetSetting(ctx, "history", "not-a-number"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	rec := h.authed(http.MethodGet, "/api/v1/flags", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("global listing with a corrupt setting = %d, want 200", rec.Code)
	}
	var list apitypes.FlagList
	h.decode(rec, &list)
	history := getFlag(t, list, "history")
	if history.Global != nil {
		t.Errorf("corrupt global value surfaced as %v, want absent", *history.Global)
	}
	// Other flags are still reported.
	if v := getFlag(t, list, "notify_threshold").Default; v != float64(1) {
		t.Errorf("notify_threshold default = %v, want 1", v)
	}

	// With a corrupt row present, the watch still resolves (to defaults)
	// rather than 500ing.
	rec = h.authed(http.MethodGet, "/api/v1/watches/"+id+"/flags", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("watch listing with a corrupt global = %d, want 200", rec.Code)
	}
	h.decode(rec, &list)
	if v := getFlag(t, list, "history").Effective; v == nil || *v != float64(1) {
		t.Errorf("effective history with a corrupt global = %v, want the default 1", v)
	}

	// An unknown stored value (only writable out of band) degrades the same way.
	if err := h.store.SetWatchFlag(ctx, id, "bogus_flag", 3); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	if rec = h.authed(http.MethodGet, "/api/v1/watches/"+id, nil); rec.Code != http.StatusOK {
		t.Fatalf("watch GET with an unknown stored flag = %d, want 200", rec.Code)
	}
	if rec = h.authed(http.MethodGet, "/api/v1/watches/"+id+"/flags", nil); rec.Code != http.StatusOK {
		t.Fatalf("watch flag listing with an unknown stored flag = %d, want 200", rec.Code)
	}

	// And a write still succeeds while the poisoned row is present.
	if rec = h.authed(http.MethodPut, "/api/v1/flags/notify_threshold", apitypes.SetFlagRequest{Value: 2}); rec.Code != 200 {
		t.Fatalf("global set with a corrupt sibling row = %d, want 200", rec.Code)
	}
}

// ---------- 4. toAPIWatch ----------

// TestAdversarialStringScalarsAreAccepted pins the documented "strings are
// accepted for both types" rule at the API boundary, since the wire is not
// limited to the CLI's typed values.
func TestAdversarialStringScalarsAreAccepted(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	pat := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{Patterns: []string{"v*"}})
	patID := pat.Watches[0].ID

	if rec := h.authed(http.MethodPut, "/api/v1/flags/history",
		apitypes.SetFlagRequest{Value: "5"}); rec.Code != 200 {
		t.Fatalf(`PUT history "5" = %d, want 200 (body %s)`, rec.Code, rec.Body.String())
	}
	if v := getFlag(t, h.flagsOf(""), "history").Global; v == nil || *v != float64(5) {
		t.Errorf(`history "5" stored as %v, want 5`, v)
	}
	if rec := h.authed(http.MethodPut, "/api/v1/watches/"+patID+"/flags/semver",
		apitypes.SetFlagRequest{Value: "true"}); rec.Code != 200 {
		t.Fatalf(`PUT semver "true" = %d, want 200 (body %s)`, rec.Code, rec.Body.String())
	}
	if v := getFlag(t, h.flagsOf(patID), "semver").Effective; v == nil || *v != true {
		t.Errorf(`semver "true" resolved to %v, want true`, v)
	}

	// A pattern watch's wire form carries the pattern-only keys; a tag watch
	// omits them (asserted elsewhere).
	rec := h.authed(http.MethodGet, "/api/v1/watches/"+patID, nil)
	var wire apitypes.Watch
	h.decode(rec, &wire)
	for _, name := range []string{"semver", "semver_stable", "semver_notify_threshold"} {
		if _, ok := wire.Flags[name]; !ok {
			t.Errorf("pattern watch wire is missing %s: %v", name, wire.Flags)
		}
	}
	if wire.Flags["semver"] != true {
		t.Errorf("pattern watch semver = %v, want true", wire.Flags["semver"])
	}
}

// TestAdversarialWatchWireFlagsAcrossPaths checks Flags/FlagsLocal on create,
// list, get and patch, with and without overrides, and that a later global
// change is reflected in existing watches.
func TestAdversarialWatchWireFlagsAcrossPaths(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())

	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := created.Watches[0].ID

	// No overrides: flags holds the defaults, flags_local is absent.
	first := created.Watches[0]
	if first.Flags["history"] != float64(1) || first.Flags["notify_threshold"] != float64(1) {
		t.Errorf("create flags = %v, want defaults", first.Flags)
	}
	if len(first.FlagsLocal) != 0 {
		t.Errorf("create flags_local = %v, want empty", first.FlagsLocal)
	}

	paths := []struct {
		name string
		got  func() apitypes.Watch
	}{
		{"list", func() apitypes.Watch {
			rec := h.authed(http.MethodGet, "/api/v1/watches", nil)
			var list apitypes.List[apitypes.Watch]
			h.decode(rec, &list)
			if len(list.Items) != 1 {
				t.Fatalf("list returned %d watches", len(list.Items))
			}
			return list.Items[0]
		}},
		{"get", func() apitypes.Watch {
			rec := h.authed(http.MethodGet, "/api/v1/watches/"+id, nil)
			var w apitypes.Watch
			h.decode(rec, &w)
			return w
		}},
		{"patch", func() apitypes.Watch {
			rec := h.authed(http.MethodPatch, "/api/v1/watches/"+id, apitypes.UpdateWatchRequest{Enabled: boolPtr(true)})
			var w apitypes.Watch
			h.decode(rec, &w)
			return w
		}},
	}
	for _, p := range paths {
		w := p.got()
		if w.Flags["history"] != float64(1) {
			t.Errorf("%s flags = %v, want history 1", p.name, w.Flags)
		}
		if len(w.FlagsLocal) != 0 {
			t.Errorf("%s flags_local = %v, want empty", p.name, w.FlagsLocal)
		}
	}

	// A visible global change is reflected in a pre-existing watch's Flags but
	// not in its FlagsLocal; every path must agree.
	if rec := h.authed(http.MethodPut, "/api/v1/flags/history", apitypes.SetFlagRequest{Value: 8}); rec.Code != 200 {
		t.Fatalf("global set = %d", rec.Code)
	}
	for _, p := range paths {
		w := p.got()
		if w.Flags["history"] != float64(8) {
			t.Errorf("%s flags after a global change = %v, want history 8", p.name, w.Flags)
		}
		if len(w.FlagsLocal) != 0 {
			t.Errorf("%s flags_local after a global change = %v, want empty", p.name, w.FlagsLocal)
		}
	}

	// With an override: Flags shows the override, FlagsLocal records it.
	if rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/history", apitypes.SetFlagRequest{Value: 3}); rec.Code != 200 {
		t.Fatalf("local set = %d", rec.Code)
	}
	for _, p := range paths {
		w := p.got()
		if w.Flags["history"] != float64(3) {
			t.Errorf("%s flags with an override = %v, want history 3", p.name, w.Flags)
		}
		if w.FlagsLocal["history"] != float64(3) {
			t.Errorf("%s flags_local with an override = %v, want history 3", p.name, w.FlagsLocal)
		}
	}
}

// ---------- 7. end-to-end wiring ----------

// TestAdversarialE2EGlobalNotifyThresholdBatchesRealChecks drives real checks
// through HTTP and asserts that a global threshold actually gates delivery.
func TestAdversarialE2EGlobalNotifyThresholdBatchesRealChecks(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	ch, recording := h.channelWithRecorder("ops")

	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{Channels: []string{ch.ID}})
	id := created.Watches[0].ID

	// Baseline check: no event, no notification.
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))
	if res := h.check(id); res.Notified != 0 || res.Changed {
		t.Fatalf("baseline check = %+v, want no change and no notification", res)
	}

	if rec := h.authed(http.MethodPut, "/api/v1/flags/notify_threshold", apitypes.SetFlagRequest{Value: 3}); rec.Code != 200 {
		t.Fatalf("global threshold set = %d", rec.Code)
	}

	// Two changes: accumulated, not delivered.
	for i := byte(2); i <= 3; i++ {
		h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(i))
		res := h.check(id)
		if !res.Changed {
			t.Fatalf("change %d: expected a digest change, got %+v", i, res)
		}
		if res.Notified != 0 {
			t.Errorf("change %d notified %d channels, want 0 (below the threshold)", i, res.Notified)
		}
	}
	if n := recording.Count(); n != 0 {
		t.Fatalf("recorded %d messages below the threshold, want 0", n)
	}

	// Third change fires exactly one batch.
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(4))
	res := h.check(id)
	if res.Notified != 1 {
		t.Fatalf("threshold-firing check notified %d channels, want 1", res.Notified)
	}
	if n := recording.Count(); n != 1 {
		t.Fatalf("recorded %d messages at the threshold, want 1", n)
	}
	msg := recording.Messages()[0]
	if !strings.Contains(msg.Title, "3 changes") {
		t.Errorf("batch title = %q, want it to summarise 3 changes", msg.Title)
	}
	if !strings.Contains(msg.Body, "Earlier changes in this batch") {
		t.Errorf("batch body does not enumerate the prior changes:\n%s", msg.Body)
	}

	// The counter reset: the next change starts a new batch.
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(5))
	if res := h.check(id); res.Notified != 0 {
		t.Errorf("post-batch change notified %d, want 0", res.Notified)
	}
	if n := recording.Count(); n != 1 {
		t.Errorf("recorded %d messages after the reset, want 1", n)
	}
}

// TestAdversarialE2EWatchNotifyThresholdOverrideBatchesRealChecks is the same
// claim for a per-watch override set through the API.
func TestAdversarialE2EWatchNotifyThresholdOverrideBatchesRealChecks(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	ch, recording := h.channelWithRecorder("ops")

	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{Channels: []string{ch.ID}})
	id := created.Watches[0].ID

	// The global value says "notify immediately"; the override must win.
	if rec := h.authed(http.MethodPut, "/api/v1/flags/notify_threshold", apitypes.SetFlagRequest{Value: 1}); rec.Code != 200 {
		t.Fatalf("global threshold set = %d", rec.Code)
	}
	if rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/notify_threshold", apitypes.SetFlagRequest{Value: 3}); rec.Code != 200 {
		t.Fatalf("watch threshold set = %d", rec.Code)
	}

	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))
	h.check(id)

	for i := byte(2); i <= 3; i++ {
		h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(i))
		if res := h.check(id); res.Notified != 0 {
			t.Errorf("change %d notified %d, want 0", i, res.Notified)
		}
	}
	if n := recording.Count(); n != 0 {
		t.Fatalf("recorded %d messages below the threshold, want 0", n)
	}

	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(4))
	if res := h.check(id); res.Notified != 1 {
		t.Fatalf("threshold-firing check notified %d, want 1", res.Notified)
	}
	if n := recording.Count(); n != 1 {
		t.Fatalf("recorded %d messages, want 1", n)
	}

	// Removing the override flushes nothing further: the counter already reset
	// and a single change now notifies immediately again.
	if rec := h.authed(http.MethodDelete, "/api/v1/watches/"+id+"/flags/notify_threshold", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unset override = %d, want 204", rec.Code)
	}
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(5))
	if res := h.check(id); res.Notified != 1 {
		t.Errorf("change after unsetting the override notified %d, want 1", res.Notified)
	}
}

// TestAdversarialE2EGlobalSemverSeedsSilentlyThenNotifiesBumps proves the
// semver flag reaches the engine through the global settings table.
func TestAdversarialE2EGlobalSemverSeedsSilentlyThenNotifiesBumps(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	ch, recording := h.channelWithRecorder("ops")

	created := h.createWatch("ghcr.io/owner/app", apitypes.CreateWatchRequest{
		Patterns: []string{"v*"}, Channels: []string{ch.ID},
	})
	id := created.Watches[0].ID

	if rec := h.authed(http.MethodPut, "/api/v1/flags/semver", apitypes.SetFlagRequest{Value: true}); rec.Code != 200 {
		t.Fatalf("global semver set = %d", rec.Code)
	}

	h.registry.SetTags("ghcr.io", "owner/app", "v1.0.0", "v1.1.0")
	h.registry.SetDigest("ghcr.io", "owner/app", "v1.1.0", testutilDigest(1))
	res := h.check(id)
	// The tag baseline is still recorded on this check, so Baseline is true;
	// what matters is that the seeding is silent.
	if res.EventID != "" {
		t.Errorf("the seeding check produced event %q, want none", res.EventID)
	}
	if n := recording.Count(); n != 0 {
		t.Fatalf("the seeding check notified %d times, want 0", n)
	}
	if ev := h.watchEvents(id); len(ev.Items) != 0 {
		t.Errorf("the seeding check recorded events: %+v", ev.Items)
	}

	// A higher version is a bump and notifies.
	h.registry.SetTags("ghcr.io", "owner/app", "v1.0.0", "v1.1.0", "v1.2.0")
	h.registry.SetDigest("ghcr.io", "owner/app", "v1.2.0", testutilDigest(2))
	res = h.check(id)
	if res.Notified != 1 {
		t.Fatalf("the bump notified %d channels, want 1 (result %+v)", res.Notified, res)
	}
	if n := recording.Count(); n != 1 {
		t.Fatalf("recorded %d messages, want 1", n)
	}
	events := h.watchEvents(id)
	if len(events.Items) != 1 || events.Items[0].Type != apitypes.EventSemverNewTag {
		t.Fatalf("events = %+v, want exactly one semver_new_tag", events.Items)
	}
}

// TestAdversarialE2EWatchSemverOverride is the same claim for a watch override.
func TestAdversarialE2EWatchSemverOverride(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	ch, recording := h.channelWithRecorder("ops")

	created := h.createWatch("ghcr.io/owner/app", apitypes.CreateWatchRequest{
		Patterns: []string{"v*"}, Channels: []string{ch.ID},
	})
	id := created.Watches[0].ID

	if rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/semver", apitypes.SetFlagRequest{Value: true}); rec.Code != 200 {
		t.Fatalf("watch semver set = %d", rec.Code)
	}

	h.registry.SetTags("ghcr.io", "owner/app", "v1.0.0", "v1.1.0")
	h.check(id)
	if n := recording.Count(); n != 0 {
		t.Fatalf("the seeding check notified %d times, want 0", n)
	}

	h.registry.SetTags("ghcr.io", "owner/app", "v1.0.0", "v1.1.0", "v1.2.0")
	res := h.check(id)
	if res.Notified != 1 || recording.Count() != 1 {
		t.Fatalf("bump notified %d (recorded %d), want 1", res.Notified, recording.Count())
	}
	if events := h.watchEvents(id); len(events.Items) != 1 || events.Items[0].Type != apitypes.EventSemverNewTag {
		t.Fatalf("events = %+v, want exactly one semver_new_tag", events.Items)
	}

	// Turning semver off resumes new-tag reporting without re-reporting the
	// tags it already knew about.
	if rec := h.authed(http.MethodDelete, "/api/v1/watches/"+id+"/flags/semver", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unset semver = %d", rec.Code)
	}
	res = h.check(id)
	if res.Notified != 0 {
		t.Errorf("the first check after leaving semver mode notified %d, want 0", res.Notified)
	}
}

// TestAdversarialE2EHistoryRetainsNRealEvents proves the history flag is
// actually consumed by the engine, measured through GET /api/v1/events.
func TestAdversarialE2EHistoryRetainsNRealEvents(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	ch, _ := h.channelWithRecorder("ops")

	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{Channels: []string{ch.ID}})
	id := created.Watches[0].ID

	if rec := h.authed(http.MethodPut, "/api/v1/flags/history", apitypes.SetFlagRequest{Value: 2}); rec.Code != 200 {
		t.Fatalf("global history set = %d", rec.Code)
	}

	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))
	h.check(id) // baseline, no event

	for i := byte(2); i <= 4; i++ {
		h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(i))
		h.check(id)
	}

	list := h.watchEvents(id)
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("retained %d/%d events with history=2, want 2", len(list.Items), list.Total)
	}
	// The retained pair must be the newest two.
	if list.Items[0].NewDigest != testutilDigest(4) || list.Items[1].NewDigest != testutilDigest(3) {
		t.Errorf("retained events = %s / %s, want digests 4 then 3",
			list.Items[0].NewDigest, list.Items[1].NewDigest)
	}

	// A watch-scoped override beats the global value.
	if rec := h.authed(http.MethodPut, "/api/v1/flags/history", apitypes.SetFlagRequest{Value: 3}); rec.Code != 200 {
		t.Fatalf("global history set = %d", rec.Code)
	}
	if rec := h.authed(http.MethodPut, "/api/v1/watches/"+id+"/flags/history", apitypes.SetFlagRequest{Value: 1}); rec.Code != 200 {
		t.Fatalf("watch history set = %d", rec.Code)
	}
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(5))
	h.check(id)
	if list = h.watchEvents(id); len(list.Items) != 1 {
		t.Fatalf("retained %d events with a local history override of 1, want 1", len(list.Items))
	}
}
