package server

import (
	"net/http"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/registry"
)

func TestCreateWatchSuccess(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})

	resp := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	if len(resp.Created) != 1 {
		t.Fatalf("Created = %v, want 1 watch", resp.Created)
	}
	if resp.Registry != "ghcr.io" {
		t.Errorf("Registry = %q, want ghcr.io", resp.Registry)
	}
	watch := resp.Watches[0]
	if watch.Kind != apitypes.WatchKindTag {
		t.Errorf("Kind = %q, want tag", watch.Kind)
	}
	if watch.Ref != "v1" {
		t.Errorf("Ref = %q, want v1", watch.Ref)
	}
	if !watch.Enabled {
		t.Error("Enabled = false, want true")
	}
	if !watch.NotifyOnFailure {
		t.Error("NotifyOnFailure = false, want true by default")
	}
	if watch.Repository != "owner/app" {
		t.Errorf("Repository = %q, want owner/app", watch.Repository)
	}
}

func TestCreateWatchNormalisesDockerHub(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("index.docker.io", "library/nginx", registry.ProbeResult{Exists: true})

	resp := h.createWatch("nginx:1.27", apitypes.CreateWatchRequest{})
	if resp.Registry != "index.docker.io" {
		t.Errorf("Registry = %q, want index.docker.io", resp.Registry)
	}
	if resp.Watches[0].Repository != "library/nginx" {
		t.Errorf("Repository = %q, want library/nginx", resp.Watches[0].Repository)
	}
	if resp.Watches[0].Ref != "1.27" {
		t.Errorf("Ref = %q, want 1.27", resp.Watches[0].Ref)
	}
}

func TestCreateWatchDefaultsToLatest(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})

	resp := h.createWatch("ghcr.io/owner/app", apitypes.CreateWatchRequest{})
	if resp.Watches[0].Ref != "latest" {
		t.Errorf("Ref = %q, want latest for a bare image reference", resp.Watches[0].Ref)
	}
}

func TestCreateWatchOnePerTag(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})

	resp := h.createWatch("ghcr.io/owner/app", apitypes.CreateWatchRequest{
		Tags: []string{"v1", "v2", "v3"},
	})
	if len(resp.Created) != 3 {
		t.Fatalf("Created %d watches, want 3", len(resp.Created))
	}
	refs := map[string]bool{}
	for _, w := range resp.Watches {
		refs[w.Ref] = true
	}
	for _, want := range []string{"v1", "v2", "v3"} {
		if !refs[want] {
			t.Errorf("no watch created for %q", want)
		}
	}
}

func TestCreateWatchPattern(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})

	resp := h.createWatch("ghcr.io/owner/app", apitypes.CreateWatchRequest{
		Patterns: []string{"v1.*"},
	})
	if len(resp.Created) != 1 {
		t.Fatalf("Created %d watches, want 1", len(resp.Created))
	}
	if resp.Watches[0].Kind != apitypes.WatchKindPattern {
		t.Errorf("Kind = %q, want pattern", resp.Watches[0].Kind)
	}
	if resp.Watches[0].Ref != "v1.*" {
		t.Errorf("Ref = %q, want v1.*", resp.Watches[0].Ref)
	}
}

func TestCreateWatchDuplicateIsReported(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})

	h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	// The second request is not an error: it reports the existing watch.
	rec := h.authed(http.MethodPost, "/api/v1/watches", apitypes.CreateWatchRequest{
		Image: "ghcr.io/owner/app:v1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when everything already exists", rec.Code)
	}
	var resp apitypes.CreateWatchResponse
	h.decode(rec, &resp)
	if len(resp.Created) != 0 {
		t.Errorf("Created = %v, want none", resp.Created)
	}
	if len(resp.Existing) != 1 || resp.Existing[0] != "v1" {
		t.Errorf("Existing = %v, want [v1]", resp.Existing)
	}
}

func TestCreateWatchValidation(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})

	cases := []struct {
		name string
		req  apitypes.CreateWatchRequest
		want string
	}{
		{
			name: "tag and pattern together",
			req:  apitypes.CreateWatchRequest{Image: "ghcr.io/owner/app", Tags: []string{"v1"}, Patterns: []string{"v2.*"}},
			want: apitypes.CodeValidationFailed,
		},
		{
			name: "invalid tag",
			req:  apitypes.CreateWatchRequest{Image: "ghcr.io/owner/app", Tags: []string{"bad tag"}},
			want: apitypes.CodeValidationFailed,
		},
		{
			name: "invalid pattern",
			req:  apitypes.CreateWatchRequest{Image: "ghcr.io/owner/app", Patterns: []string{"a[b"}},
			want: apitypes.CodeValidationFailed,
		},
		{
			name: "unparseable image",
			req:  apitypes.CreateWatchRequest{Image: ""},
			want: apitypes.CodeValidationFailed,
		},
		{
			name: "digest reference without a tag",
			req: apitypes.CreateWatchRequest{
				Image: "ghcr.io/owner/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
			want: apitypes.CodeValidationFailed,
		},
		{
			name: "unknown channel",
			req:  apitypes.CreateWatchRequest{Image: "ghcr.io/owner/app:v1", Channels: []string{"nope"}},
			want: apitypes.CodeValidationFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.authed(http.MethodPost, "/api/v1/watches", tc.req)
			if rec.Code != apitypes.StatusCode(tc.want) {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, apitypes.StatusCode(tc.want), rec.Body.String())
			}
			if code := h.errorCode(rec); code != tc.want {
				t.Errorf("error code = %q, want %q", code, tc.want)
			}
		})
	}
}

// TestCreateWatchCredentialsRequired428 is the prompt-and-retry trigger: a
// registry that answers 401 anonymously must produce a 428 naming the host.
func TestCreateWatchCredentialsRequired428(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbeErr("ghcr.io", "owner/app", &registry.Error{
		Kind:       registry.KindUnauthorized,
		StatusCode: http.StatusUnauthorized,
		Registry:   "ghcr.io",
		Repository: "owner/app",
	})

	rec := h.authed(http.MethodPost, "/api/v1/watches", apitypes.CreateWatchRequest{
		Image: "ghcr.io/owner/app:v1",
	})
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428", rec.Code)
	}
	var envelope apitypes.ErrorEnvelope
	h.decode(rec, &envelope)
	if envelope.Error.Code != apitypes.CodeCredentialsRequired {
		t.Errorf("error code = %q, want %q", envelope.Error.Code, apitypes.CodeCredentialsRequired)
	}
	if envelope.Error.Registry != "ghcr.io" {
		t.Errorf("error registry = %q, want ghcr.io so the CLI knows what to prompt for", envelope.Error.Registry)
	}
	// The message must explain the Docker Hub ambiguity.
	if !contains(envelope.Error.Message, "does not exist") {
		t.Errorf("message does not explain the private-vs-nonexistent ambiguity: %q", envelope.Error.Message)
	}
}

// TestCredentialsRequiredFlowStoresAndRetries walks the whole flow: 428, then
// the credentials are stored and the same request succeeds.
func TestCredentialsRequiredFlowStoresAndRetries(t *testing.T) {
	h := newHarness(t)

	// First attempt: the registry rejects anonymous access.
	h.registry.SetProbeErr("ghcr.io", "owner/app", &registry.Error{
		Kind: registry.KindUnauthorized, StatusCode: http.StatusUnauthorized,
		Registry: "ghcr.io", Repository: "owner/app",
	})
	rec := h.authed(http.MethodPost, "/api/v1/watches", apitypes.CreateWatchRequest{
		Image: "ghcr.io/owner/app:v1",
	})
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428", rec.Code)
	}

	// The CLI stores credentials for the registry the error named.
	putRec := h.authed(http.MethodPut, "/api/v1/registries/ghcr.io/credentials",
		apitypes.CredentialsRequest{Username: "andriotis", Password: "ghp_secret"})
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT credentials: status %d, body %s", putRec.Code, putRec.Body.String())
	}

	// The registry now accepts the credentials.
	h.registry.SetProbeErr("ghcr.io", "owner/app", nil)
	h.registry.ProbeResults["ghcr.io/owner/app"] = registry.ProbeResult{Exists: true}

	// The retry succeeds.
	resp := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	if len(resp.Created) != 1 {
		t.Fatalf("the retry created %d watches, want 1", len(resp.Created))
	}
}

func TestCreateWatchUpstreamFailure(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbeErr("ghcr.io", "owner/app", &registry.Error{
		Kind: registry.KindTransient, Registry: "ghcr.io", Repository: "owner/app",
	})

	rec := h.authed(http.MethodPost, "/api/v1/watches", apitypes.CreateWatchRequest{
		Image: "ghcr.io/owner/app:v1",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if code := h.errorCode(rec); code != apitypes.CodeUpstreamError {
		t.Errorf("error code = %q, want %q", code, apitypes.CodeUpstreamError)
	}
}

func TestCreateWatchNotFoundUpstream(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbeErr("ghcr.io", "owner/app", &registry.Error{
		Kind: registry.KindNotFound, Registry: "ghcr.io", Repository: "owner/app",
	})

	rec := h.authed(http.MethodPost, "/api/v1/watches", apitypes.CreateWatchRequest{
		Image: "ghcr.io/owner/app:v1",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for a missing repository", rec.Code)
	}
}

func TestListWatchesFilters(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	h.registry.SetProbe("quay.io", "team/app", registry.ProbeResult{Exists: true})

	ghcr := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	quay := h.createWatch("quay.io/team/app:latest", apitypes.CreateWatchRequest{})
	h.authed(http.MethodPatch, "/api/v1/watches/"+quay.Created[0], apitypes.UpdateWatchRequest{
		Enabled: boolPtr(false),
	})

	// Unfiltered.
	rec := h.authed(http.MethodGet, "/api/v1/watches", nil)
	var all apitypes.List[apitypes.Watch]
	h.decode(rec, &all)
	if all.Total != 2 {
		t.Errorf("total = %d, want 2", all.Total)
	}

	// Enabled only.
	rec = h.authed(http.MethodGet, "/api/v1/watches?enabled=true", nil)
	var enabled apitypes.List[apitypes.Watch]
	h.decode(rec, &enabled)
	if enabled.Total != 1 || enabled.Items[0].ID != ghcr.Created[0] {
		t.Errorf("enabled filter returned %+v, want only the ghcr watch", enabled.Items)
	}

	// By registry.
	rec = h.authed(http.MethodGet, "/api/v1/watches?registry=quay.io", nil)
	var byRegistry apitypes.List[apitypes.Watch]
	h.decode(rec, &byRegistry)
	if byRegistry.Total != 1 || byRegistry.Items[0].Registry != "quay.io" {
		t.Errorf("registry filter returned %+v, want only quay.io", byRegistry.Items)
	}

	// By search term.
	rec = h.authed(http.MethodGet, "/api/v1/watches?q=team", nil)
	var searched apitypes.List[apitypes.Watch]
	h.decode(rec, &searched)
	if searched.Total != 1 {
		t.Errorf("search filter total = %d, want 1", searched.Total)
	}

	// Invalid boolean.
	rec = h.authed(http.MethodGet, "/api/v1/watches?enabled=maybe", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d for an invalid boolean, want 400", rec.Code)
	}
}

func TestGetUpdateDeleteWatch(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := created.Created[0]

	// Read.
	rec := h.authed(http.MethodGet, "/api/v1/watches/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET watch: status %d", rec.Code)
	}
	var watch apitypes.Watch
	h.decode(rec, &watch)
	if watch.ID != id {
		t.Errorf("ID = %q, want %q", watch.ID, id)
	}

	// Unknown ID.
	rec = h.authed(http.MethodGet, "/api/v1/watches/w_missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET unknown watch: status %d, want 404", rec.Code)
	}

	// Patch.
	rec = h.authed(http.MethodPatch, "/api/v1/watches/"+id, apitypes.UpdateWatchRequest{
		Enabled:         boolPtr(false),
		NotifyOnFailure: boolPtr(false),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH watch: status %d, body %s", rec.Code, rec.Body.String())
	}
	h.decode(rec, &watch)
	if watch.Enabled || watch.NotifyOnFailure {
		t.Errorf("watch = %+v, want both flags false", watch)
	}

	// Delete.
	rec = h.authed(http.MethodDelete, "/api/v1/watches/"+id, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE watch: status %d, want 204", rec.Code)
	}
	rec = h.authed(http.MethodGet, "/api/v1/watches/"+id, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete: status %d, want 404", rec.Code)
	}
	rec = h.authed(http.MethodDelete, "/api/v1/watches/"+id, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("second DELETE: status %d, want 404", rec.Code)
	}
}

func TestUpdateWatchChannels(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := created.Created[0]

	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create channel: status %d, body %s", rec.Code, rec.Body.String())
	}
	var channel apitypes.Channel
	h.decode(rec, &channel)

	// Subscribe by name.
	rec = h.authed(http.MethodPatch, "/api/v1/watches/"+id, apitypes.UpdateWatchRequest{
		Channels: &[]string{"ops"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH channels: status %d, body %s", rec.Code, rec.Body.String())
	}
	var watch apitypes.Watch
	h.decode(rec, &watch)
	if len(watch.Channels) != 1 || watch.Channels[0] != channel.ID {
		t.Errorf("Channels = %v, want [%s]", watch.Channels, channel.ID)
	}

	// An unknown channel name is a validation error.
	rec = h.authed(http.MethodPatch, "/api/v1/watches/"+id, apitypes.UpdateWatchRequest{
		Channels: &[]string{"does-not-exist"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d for an unknown channel, want 422", rec.Code)
	}
}

func TestCheckWatchEndpoint(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := created.Created[0]
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))

	// First check records a baseline.
	rec := h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("check: status %d, body %s", rec.Code, rec.Body.String())
	}
	var result apitypes.CheckResult
	h.decode(rec, &result)
	if !result.Baseline {
		t.Error("Baseline = false on the first check")
	}
	if result.Changed {
		t.Error("Changed = true on the first check")
	}

	// The digest changes: the next check reports it.
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(2))
	rec = h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)
	h.decode(rec, &result)
	if !result.Changed {
		t.Fatal("Changed = false after the digest changed")
	}
	if result.EventID == "" {
		t.Error("EventID is empty after a change")
	}

	// Unknown watch.
	rec = h.authed(http.MethodPost, "/api/v1/watches/w_missing/check", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("check on an unknown watch: status %d, want 404", rec.Code)
	}
}

func TestWatchEventsEndpoint(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := created.Created[0]

	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))
	h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(2))
	h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)

	rec := h.authed(http.MethodGet, "/api/v1/watches/"+id+"/events", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var events apitypes.List[apitypes.Event]
	h.decode(rec, &events)
	if events.Total != 1 {
		t.Fatalf("total = %d, want 1 digest_changed event", events.Total)
	}
	if events.Items[0].Type != apitypes.EventDigestChanged {
		t.Errorf("event type = %q, want digest_changed", events.Items[0].Type)
	}
	if events.Items[0].Image == "" {
		t.Error("the event does not carry the image name")
	}

	// An unknown watch is a 404, not an empty page.
	rec = h.authed(http.MethodGet, "/api/v1/watches/w_missing/events", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("events for an unknown watch: status %d, want 404", rec.Code)
	}
}

func TestGlobalEventsFiltering(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	id := created.Created[0]

	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))
	h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(2))
	h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)

	rec := h.authed(http.MethodGet, "/api/v1/events", nil)
	var all apitypes.List[apitypes.Event]
	h.decode(rec, &all)
	if all.Total != 1 {
		t.Errorf("global feed total = %d, want 1", all.Total)
	}

	rec = h.authed(http.MethodGet, "/api/v1/events?type=digest_changed", nil)
	var filtered apitypes.List[apitypes.Event]
	h.decode(rec, &filtered)
	if filtered.Total != 1 {
		t.Errorf("filtered feed total = %d, want 1", filtered.Total)
	}

	rec = h.authed(http.MethodGet, "/api/v1/events?type=new_tags", nil)
	h.decode(rec, &filtered)
	if filtered.Total != 0 {
		t.Errorf("filtered feed total = %d, want 0", filtered.Total)
	}

	// An unknown type is a validation error rather than an empty page.
	rec = h.authed(http.MethodGet, "/api/v1/events?type=nonsense", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d for an unknown event type, want 422", rec.Code)
	}

	rec = h.authed(http.MethodGet, "/api/v1/events?watch="+id, nil)
	h.decode(rec, &filtered)
	if filtered.Total != 1 {
		t.Errorf("per-watch feed total = %d, want 1", filtered.Total)
	}
}

func TestNotificationsEndpoint(t *testing.T) {
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
	h.authed(http.MethodPatch, "/api/v1/channels/"+channel.ID, apitypes.UpdateChannelRequest{
		IsDefault: boolPtr(true),
	})
	h.notifier.Set(channel.ID, string(apitypes.ChannelNtfy))

	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))
	h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(2))
	h.authed(http.MethodPost, "/api/v1/watches/"+id+"/check", nil)

	rec = h.authed(http.MethodGet, "/api/v1/notifications", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var list apitypes.List[apitypes.Notification]
	h.decode(rec, &list)
	if list.Total != 1 {
		t.Fatalf("total = %d, want 1 delivered notification", list.Total)
	}
	if list.Items[0].Status != apitypes.NotificationSent {
		t.Errorf("status = %q, want sent", list.Items[0].Status)
	}
	if list.Items[0].ChannelName != "ops" {
		t.Errorf("channel name = %q, want ops", list.Items[0].ChannelName)
	}
}

func boolPtr(v bool) *bool { return &v }
