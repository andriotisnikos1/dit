package server

// Adversarial probe for the apiclient transport: the real typed client against
// the real handler. The CLI test harness substitutes a stub API, so nothing
// else in the suite can prove the request paths, methods or body shapes.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apiclient"
	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestAdversarialAPIClientAgainstRealServer drives the real client against the
// real handler, so URL escaping, method and body shapes are exercised end to
// end (the CLI stub cannot prove any of that).
func TestAdversarialAPIClientAgainstRealServer(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := created.Watches[0].ID

	srv := httptest.NewServer(h.handler)
	defer srv.Close()

	client, err := apiclient.New(srv.URL, testToken)
	if err != nil {
		t.Fatalf("apiclient.New: %v", err)
	}
	ctx := context.Background()

	list, err := client.ListFlags(ctx, "")
	if err != nil {
		t.Fatalf("ListFlags global: %v", err)
	}
	if len(list) != 5 {
		t.Fatalf("ListFlags returned %d flags, want 5", len(list))
	}
	if list[0].Name == "" {
		t.Errorf("flag entry has no name: %+v", list[0])
	}

	if _, err := client.SetFlag(ctx, "", "history", 4); err != nil {
		t.Fatalf("SetFlag global: %v", err)
	}
	global, err := client.ListFlags(ctx, "")
	if err != nil {
		t.Fatalf("ListFlags after set: %v", err)
	}
	if v := getFlag(t, global, "history").Global; v == nil || *v != float64(4) {
		t.Errorf("global history over the client = %v, want 4", v)
	}

	// Watch-scoped calls must reach the watch route, not the global one.
	flag, err := client.SetFlag(ctx, id, "history", 7)
	if err != nil {
		t.Fatalf("SetFlag on a watch: %v", err)
	}
	if flag.Local == nil || *flag.Local != float64(7) {
		t.Errorf("watch SetFlag local = %v, want 7", flag.Local)
	}
	scoped, err := client.ListFlags(ctx, id)
	if err != nil {
		t.Fatalf("ListFlags watch: %v", err)
	}
	if v := getFlag(t, scoped, "history").Local; v == nil || *v != float64(7) {
		t.Errorf("watch history over the client = %v, want 7", v)
	}
	// The global value was not touched by the watch-scoped write.
	global, _ = client.ListFlags(ctx, "")
	if v := getFlag(t, global, "history").Global; *v != float64(4) {
		t.Errorf("a watch-scoped set changed the global value to %v", *v)
	}

	if err := client.UnsetFlag(ctx, id, "history"); err != nil {
		t.Fatalf("UnsetFlag on a watch: %v", err)
	}
	// DELETE is idempotent, including over the client's empty-body path.
	if err := client.UnsetFlag(ctx, id, "history"); err != nil {
		t.Errorf("second UnsetFlag: %v", err)
	}
	if err := client.UnsetFlag(ctx, "", "history"); err != nil {
		t.Errorf("global UnsetFlag: %v", err)
	}
	after, err := client.ListFlags(ctx, id)
	if err != nil {
		t.Fatalf("ListFlags after unset: %v", err)
	}
	if v := getFlag(t, after, "history").Local; v != nil {
		t.Errorf("the override survived the unset: %v", *v)
	}

	// Errors propagate with their code and message.
	_, err = client.SetFlag(ctx, "", "nope", 1)
	var apiErr apitypes.APIError
	if err == nil || !asAPIError(err, &apiErr) || apiErr.Code != apitypes.CodeNotFound {
		t.Errorf("unknown flag error = %v, want a not_found APIError", err)
	}
	_, err = client.SetFlag(ctx, "", "history", 0)
	if err == nil || !asAPIError(err, &apiErr) || apiErr.Code != apitypes.CodeValidationFailed {
		t.Errorf("history=0 error = %v, want a validation_failed APIError", err)
	}
	if !strings.Contains(err.Error(), "must not be 0") {
		t.Errorf("validation message = %q, want the parser's own text", err.Error())
	}
	if _, err = client.ListFlags(ctx, "w_missing"); err == nil || !asAPIError(err, &apiErr) || apiErr.Code != apitypes.CodeNotFound {
		t.Errorf("unknown watch error = %v, want a not_found APIError", err)
	}
	_, err = client.SetFlag(ctx, "w_missing", "history", 3)
	if err == nil || !asAPIError(err, &apiErr) || apiErr.Code != apitypes.CodeNotFound {
		t.Fatalf("unknown watch set error = %v, want a not_found APIError", err)
	}
	// The rejected watch-scoped write must not have fallen through to the
	// global route.
	if global, err = client.ListFlags(ctx, ""); err == nil {
		if v := getFlag(t, global, "history"); v.Global != nil {
			t.Errorf("a rejected watch-scoped set wrote the global value: %v", *v.Global)
		}
	}

	// Awkward IDs must be escaped, not routed somewhere else. A non-existent
	// watch is a 404 whichever segment the router lands on, so this pins the
	// escape/route contract rather than a specific handler.
	for _, weird := range []string{"w with space", "w/../w", "w_ünïcode", "w?query=1", "w#frag", "w_%2F_x"} {
		if _, err := client.ListFlags(ctx, weird); err == nil {
			t.Errorf("ListFlags(%q) succeeded, want not_found", weird)
		} else if !asAPIError(err, &apiErr) || apiErr.Code != apitypes.CodeNotFound {
			t.Errorf("ListFlags(%q) error = %v, want a not_found APIError", weird, err)
		}
		if err := client.UnsetFlag(ctx, weird, "history"); err == nil {
			t.Errorf("UnsetFlag(%q) succeeded, want not_found", weird)
		}
	}
}
