package cli

import (
	"context"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestNotificationsRetryCommand guards the recovery path from the CLI side.
func TestNotificationsRetryCommand(t *testing.T) {
	h := newCLIHarness(t)
	var gotID string
	h.api.retryFn = func(_ context.Context, id string) (apitypes.NotificationRetryResult, error) {
		gotID = id
		return apitypes.NotificationRetryResult{
			OK: true,
			Notification: apitypes.Notification{
				ID: id, ChannelName: "ops", Status: apitypes.NotificationSent, Attempts: 1,
			},
		}, nil
	}

	out := h.mustRun("notifications", "retry", "n_1")
	if gotID != "n_1" {
		t.Errorf("retried %q, want n_1", gotID)
	}
	if !contains(out, "Re-delivered n_1 to ops") {
		t.Errorf("output = %q, want it to report the re-delivery", out)
	}
}

// TestNotificationsRetryReportsFailure guards that a failed retry surfaces as a
// command error rather than a silent success.
func TestNotificationsRetryReportsFailure(t *testing.T) {
	h := newCLIHarness(t)
	h.api.retryFn = func(_ context.Context, id string) (apitypes.NotificationRetryResult, error) {
		return apitypes.NotificationRetryResult{
			OK:           false,
			Error:        "smtp refused",
			Notification: apitypes.Notification{ID: id, Status: apitypes.NotificationFailed},
		}, nil
	}

	err := h.run("notifications", "retry", "n_1")
	if err == nil {
		t.Fatal("expected an error for a failed retry")
	}
	if !contains(err.Error(), "smtp refused") {
		t.Errorf("error = %v, want the transport error", err)
	}
}

// TestNotificationsRetryRequiresAnID guards the argument count.
func TestNotificationsRetryRequiresAnID(t *testing.T) {
	h := newCLIHarness(t)
	if err := h.run("notifications", "retry"); err == nil {
		t.Error("expected an error with no notification ID")
	}
}

// contains is a local helper so the cli tests stay dependency-free.
func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
