package server

import (
	"net/http"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/registry"
)

// TestGlobalEventsWatchFilterActuallyFilters guards the CLI's `dit events
// --watch <id>`, which sends the watch as a query parameter. The per-watch
// endpoint takes it from the path, so a shared implementation must read the
// query parameter too; otherwise the filter silently returns everything.
func TestGlobalEventsWatchFilterActuallyFilters(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	h.registry.SetProbe("quay.io", "team/app", registry.ProbeResult{Exists: true})

	first := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	second := h.createWatch("quay.io/team/app:v1", apitypes.CreateWatchRequest{})

	// Give both watches a digest change, so the feed has two events.
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))
	h.registry.SetDigest("quay.io", "team/app", "v1", testutilDigest(1))
	h.authed(http.MethodPost, "/api/v1/watches/"+first.Created[0]+"/check", nil)
	h.authed(http.MethodPost, "/api/v1/watches/"+second.Created[0]+"/check", nil)
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(2))
	h.registry.SetDigest("quay.io", "team/app", "v1", testutilDigest(2))
	h.authed(http.MethodPost, "/api/v1/watches/"+first.Created[0]+"/check", nil)
	h.authed(http.MethodPost, "/api/v1/watches/"+second.Created[0]+"/check", nil)

	// Unfiltered: both events.
	rec := h.authed(http.MethodGet, "/api/v1/events", nil)
	var all apitypes.List[apitypes.Event]
	h.decode(rec, &all)
	if all.Total != 2 {
		t.Fatalf("unfiltered total = %d, want 2", all.Total)
	}

	// Filtered by the `watch` query parameter, which is what the CLI sends.
	rec = h.authed(http.MethodGet, "/api/v1/events?watch="+first.Created[0], nil)
	var filtered apitypes.List[apitypes.Event]
	h.decode(rec, &filtered)
	if filtered.Total != 1 {
		t.Fatalf("?watch= total = %d, want 1: the filter must be applied", filtered.Total)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].WatchID != first.Created[0] {
		t.Errorf("?watch= returned %+v, want only the first watch's event", filtered.Items)
	}

	// Combined with a type filter.
	rec = h.authed(http.MethodGet,
		"/api/v1/events?watch="+first.Created[0]+"&type=digest_changed", nil)
	h.decode(rec, &filtered)
	if filtered.Total != 1 {
		t.Errorf("combined filter total = %d, want 1", filtered.Total)
	}

	// A type the first watch never produced yields nothing.
	rec = h.authed(http.MethodGet,
		"/api/v1/events?watch="+first.Created[0]+"&type=new_tags", nil)
	h.decode(rec, &filtered)
	if filtered.Total != 0 {
		t.Errorf("combined filter total = %d, want 0", filtered.Total)
	}

	// The per-watch endpoint agrees with the query-parameter form.
	rec = h.authed(http.MethodGet, "/api/v1/watches/"+first.Created[0]+"/events", nil)
	h.decode(rec, &filtered)
	if filtered.Total != 1 {
		t.Errorf("per-watch endpoint total = %d, want 1", filtered.Total)
	}
}

// TestListWatchesPaginationTotalIsNotThePageSize guards the CLI's reliance on
// the total describing the whole filtered set.
func TestListWatchesPaginationTotalIsNotThePageSize(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	h.createWatch("ghcr.io/owner/app", apitypes.CreateWatchRequest{
		Tags: []string{"v1", "v2", "v3"},
	})

	rec := h.authed(http.MethodGet, "/api/v1/watches?limit=1", nil)
	var page apitypes.List[apitypes.Watch]
	h.decode(rec, &page)
	if len(page.Items) != 1 {
		t.Errorf("page size = %d, want 1", len(page.Items))
	}
	if page.Total != 3 {
		t.Errorf("total = %d, want 3: the total describes the whole set", page.Total)
	}
	if page.Limit != 1 {
		t.Errorf("limit = %d, want 1 echoed back", page.Limit)
	}

	// An offset past the end yields an empty page but the same total.
	rec = h.authed(http.MethodGet, "/api/v1/watches?limit=1&offset=99", nil)
	h.decode(rec, &page)
	if len(page.Items) != 0 {
		t.Errorf("items = %v, want empty past the end", page.Items)
	}
	if page.Total != 3 {
		t.Errorf("total = %d, want 3", page.Total)
	}
	// The items slice must be JSON `[]`, not `null`: a client that iterates
	// without a nil check should not have to special-case it.
	if !contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("empty page serialized items as null: %s", rec.Body.String())
	}
}

// TestNegativePaginationIsRejected guards against a negative offset producing a
// confusing SQL error.
func TestNegativePaginationIsRejected(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{
		"/api/v1/watches?offset=-1",
		"/api/v1/events?offset=-5",
		"/api/v1/notifications?limit=abc",
	} {
		rec := h.authed(http.MethodGet, path, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", path, rec.Code)
		}
	}
}

// TestLimitIsClampedNotRejected guards against a huge limit becoming an
// unbounded query.
func TestLimitIsClampedNotRejected(t *testing.T) {
	h := newHarness(t)
	rec := h.authed(http.MethodGet, "/api/v1/watches?limit=100000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var list apitypes.List[apitypes.Watch]
	h.decode(rec, &list)
	if list.Limit != apitypes.MaxLimit {
		t.Errorf("limit = %d, want it clamped to %d", list.Limit, apitypes.MaxLimit)
	}
}

// TestDeleteWatchRemovesItsEvents guards the cascade the CLI's event history
// depends on.
func TestDeleteWatchRemovesItsEvents(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := created.Created[0]

	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))
	h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(2))
	h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)

	rec := h.authed(http.MethodGet, "/api/v1/events", nil)
	var before apitypes.List[apitypes.Event]
	h.decode(rec, &before)
	if before.Total != 1 {
		t.Fatalf("events before delete = %d, want 1", before.Total)
	}

	if rec := h.authed(http.MethodDelete, "/api/v1/watches/"+id, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d", rec.Code)
	}

	rec = h.authed(http.MethodGet, "/api/v1/events", nil)
	var after apitypes.List[apitypes.Event]
	h.decode(rec, &after)
	if after.Total != 0 {
		t.Errorf("events after delete = %d, want 0", after.Total)
	}
}

// TestUpdateWatchWithEmptyBodyIsANoOp guards a PATCH that changes nothing: it
// must not error and must not disturb the watch.
func TestUpdateWatchWithEmptyBodyIsANoOp(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})

	rec := h.authed(http.MethodPatch, "/api/v1/watches/"+created.Created[0], apitypes.UpdateWatchRequest{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var watch apitypes.Watch
	h.decode(rec, &watch)
	if !watch.Enabled || !watch.NotifyOnFailure {
		t.Errorf("watch = %+v, want its flags untouched", watch)
	}
}

// TestCreateWatchDisabledIsHonoured guards the --disabled flag.
func TestCreateWatchDisabledIsHonoured(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})

	resp := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{Disabled: true})
	if len(resp.Created) != 1 {
		t.Fatalf("created %d watches, want 1", len(resp.Created))
	}
	if resp.Watches[0].Enabled {
		t.Error("Enabled = true for a watch created with --disabled")
	}
}

// TestCreateWatchNotifyOnFailureFalseIsHonoured guards the flag being an
// explicit pointer rather than a plain bool that cannot be distinguished from
// its zero value.
func TestCreateWatchNotifyOnFailureFalseIsHonoured(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})

	no := false
	resp := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{NotifyOnFailure: &no})
	if resp.Watches[0].NotifyOnFailure {
		t.Error("NotifyOnFailure = true when the request explicitly said false")
	}

	// And omitting it defaults to true.
	resp = h.createWatch("ghcr.io/owner/app:v2", apitypes.CreateWatchRequest{})
	if !resp.Watches[0].NotifyOnFailure {
		t.Error("NotifyOnFailure = false when the request omitted it; the default is true")
	}
}

// TestWatchChannelsClearedMeansDefaults guards the documented meaning of an
// empty subscription list.
func TestWatchChannelsClearedMeansDefaults(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := created.Created[0]

	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	})
	var channel apitypes.Channel
	h.decode(rec, &channel)

	// Subscribe, then clear with an explicit empty array.
	rec = h.authed(http.MethodPatch, "/api/v1/watches/"+id, apitypes.UpdateWatchRequest{
		Channels: &[]string{"ops"},
	})
	var watch apitypes.Watch
	h.decode(rec, &watch)
	if len(watch.Channels) != 1 {
		t.Fatalf("Channels = %v, want one subscription", watch.Channels)
	}

	rec = h.authed(http.MethodPatch, "/api/v1/watches/"+id, apitypes.UpdateWatchRequest{
		Channels: &[]string{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("clearing channels: status %d, body %s", rec.Code, rec.Body.String())
	}
	h.decode(rec, &watch)
	if len(watch.Channels) != 0 {
		t.Errorf("Channels = %v, want none after clearing", watch.Channels)
	}

	// The watch now notifies the default set, which is empty here.
	if _, err := h.store.ChannelsForWatch(t.Context(), id); err != nil {
		t.Fatalf("ChannelsForWatch: %v", err)
	}
}
