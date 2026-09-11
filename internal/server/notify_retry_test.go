package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

// TestNotificationRetryEndpoint walks the recovery path the plan promises: a
// delivery that failed is retried through the API and succeeds once the
// channel works again.
func TestNotificationRetryEndpoint(t *testing.T) {
	h := newHarness(t)

	// A channel whose transport always fails at first.
	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	})
	var channel apitypes.Channel
	h.decode(rec, &channel)
	recording := h.notifier.Set(channel.ID, string(apitypes.ChannelNtfy))
	recording.FailFor = func(notify.Message) bool { return true }
	h.authed(http.MethodPatch, "/api/v1/channels/"+channel.ID, apitypes.UpdateChannelRequest{
		IsDefault: boolPtr(true),
	})

	// Produce an event that fails to deliver.
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))
	h.authed(http.MethodPost, "/api/v1/watches/"+created.Created[0]+"/check", nil)
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(2))
	h.authed(http.MethodPost, "/api/v1/watches/"+created.Created[0]+"/check", nil)

	rec = h.authed(http.MethodGet, "/api/v1/notifications", nil)
	var list apitypes.List[apitypes.Notification]
	h.decode(rec, &list)
	if list.Total != 1 {
		t.Fatalf("notifications = %d, want 1", list.Total)
	}
	failed := list.Items[0]
	if failed.Status != apitypes.NotificationFailed {
		t.Fatalf("status = %q, want failed", failed.Status)
	}

	// The transport recovers; the retry succeeds.
	recording.FailFor = nil
	rec = h.authed(http.MethodPost, "/api/v1/notifications/"+failed.ID+"/retry", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: status %d, body %s", rec.Code, rec.Body.String())
	}
	var result apitypes.NotificationRetryResult
	h.decode(rec, &result)
	if !result.OK {
		t.Errorf("OK = false, error %q", result.Error)
	}
	if result.Notification.Status != apitypes.NotificationSent {
		t.Errorf("status = %q after a successful retry, want sent", result.Notification.Status)
	}
	if result.Notification.LastError != "" {
		t.Errorf("LastError = %q after a successful retry, want empty", result.Notification.LastError)
	}
	if recording.Count() != 1 {
		t.Errorf("the retry delivered %d messages, want 1", recording.Count())
	}
}

// TestNotificationRetryReportsAFailure guards the other outcome: a retry that
// fails again is a 200 carrying the transport error, not a 5xx.
func TestNotificationRetryReportsAFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Build the records directly: the endpoint's contract is what is under
	// test, not the delivery path that produced them.
	watch := testutil.Watch(t, h.store, "ghcr.io", "owner/app", "v1", nil)
	channel, recording := testutil.Channel(t, h.store, h.notifier, "ops", true)
	_ = channel
	event, err := h.store.CreateEvent(ctx, store.CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "v1",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	channelRec, err := h.store.FindChannelByName(ctx, "ops")
	if err != nil {
		t.Fatalf("FindChannelByName: %v", err)
	}
	record, err := h.store.CreateNotification(ctx, event.ID, channelRec.ID)
	if err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}
	if err := h.store.UpdateNotification(ctx, record.ID, apitypes.NotificationFailed, 1, "boom"); err != nil {
		t.Fatalf("UpdateNotification: %v", err)
	}
	recording.FailFor = func(notify.Message) bool { return true }

	rec := h.authed(http.MethodPost, "/api/v1/notifications/"+record.ID+"/retry", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: the API call succeeded even though delivery did not", rec.Code)
	}
	var result apitypes.NotificationRetryResult
	h.decode(rec, &result)
	if result.OK {
		t.Error("OK = true for a retry that failed")
	}
	if result.Error == "" {
		t.Error("Error is empty for a failed retry")
	}
	if result.Notification.Status != apitypes.NotificationFailed {
		t.Errorf("status = %q, want failed", result.Notification.Status)
	}
}

// TestNotificationRetryRejectsAnAlreadySentDelivery guards against a duplicate
// notification: re-sending something the operator already received is worse
// than doing nothing.
func TestNotificationRetryRejectsAnAlreadySentDelivery(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	watch := testutil.Watch(t, h.store, "ghcr.io", "owner/app", "v1", nil)
	testutil.Channel(t, h.store, h.notifier, "ops", true)
	event, err := h.store.CreateEvent(ctx, store.CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "v1",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	channel, err := h.store.FindChannelByName(ctx, "ops")
	if err != nil {
		t.Fatalf("FindChannelByName: %v", err)
	}
	record, err := h.store.CreateNotification(ctx, event.ID, channel.ID)
	if err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}
	if err := h.store.UpdateNotification(ctx, record.ID, apitypes.NotificationSent, 1, ""); err != nil {
		t.Fatalf("UpdateNotification: %v", err)
	}

	rec := h.authed(http.MethodPost, "/api/v1/notifications/"+record.ID+"/retry", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for an already-delivered notification", rec.Code)
	}
}

// TestNotificationRetryUnknownIDIs404 guards the error path.
func TestNotificationRetryUnknownIDIs404(t *testing.T) {
	h := newHarness(t)
	rec := h.authed(http.MethodPost, "/api/v1/notifications/n_missing/retry", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestNotificationRetryRequiresAuth guards that the endpoint is behind the
// same middleware as everything else.
func TestNotificationRetryRequiresAuth(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodPost, "/api/v1/notifications/n_1/retry", nil, "\x00none")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
