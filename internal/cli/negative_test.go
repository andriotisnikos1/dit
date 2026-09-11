package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apiclient"
	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestEventsWatchFlagReachesTheQuery guards the flag that was being dropped:
// `dit events --watch <id>` must send the watch, not fetch everything.
func TestEventsWatchFlagReachesTheQuery(t *testing.T) {
	h := newCLIHarness(t)
	var got apiclient.EventQuery
	h.api.eventsFn = func(_ context.Context, q apiclient.EventQuery) (apitypes.List[apitypes.Event], error) {
		got = q
		return apitypes.List[apitypes.Event]{}, nil
	}

	h.mustRun("events", "--watch", "w_abc", "--type", "new_tags", "--limit", "7")
	if got.WatchID != "w_abc" {
		t.Errorf("WatchID = %q, want w_abc", got.WatchID)
	}
	if got.Type != apitypes.EventNewTags {
		t.Errorf("Type = %q, want new_tags", got.Type)
	}
	if got.Limit != 7 {
		t.Errorf("Limit = %d, want 7", got.Limit)
	}
}

// TestWatchListSendsEveryFilter guards each flag reaching the query, so a flag
// cannot be silently ignored.
func TestWatchListSendsEveryFilter(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		check func(*testing.T, apiclient.WatchQuery)
	}{
		{
			name: "enabled",
			args: []string{"watch", "list", "--enabled"},
			check: func(t *testing.T, q apiclient.WatchQuery) {
				if q.Enabled == nil || !*q.Enabled {
					t.Error("--enabled did not set the query")
				}
			},
		},
		{
			name: "disabled",
			args: []string{"watch", "list", "--disabled"},
			check: func(t *testing.T, q apiclient.WatchQuery) {
				if q.Enabled == nil || *q.Enabled {
					t.Error("--disabled did not set the query")
				}
			},
		},
		{
			name: "registry and search and limit",
			args: []string{"watch", "list", "--registry", "ghcr.io", "--search", "app", "--limit", "3"},
			check: func(t *testing.T, q apiclient.WatchQuery) {
				if q.Registry != "ghcr.io" {
					t.Errorf("Registry = %q", q.Registry)
				}
				if q.Search != "app" {
					t.Errorf("Search = %q", q.Search)
				}
				if q.Limit != 3 {
					t.Errorf("Limit = %d", q.Limit)
				}
			},
		},
		{
			name: "no filters",
			args: []string{"watch", "list"},
			check: func(t *testing.T, q apiclient.WatchQuery) {
				if q.Enabled != nil {
					t.Error("no filter flag must leave Enabled nil so the server returns everything")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newCLIHarness(t)
			var got apiclient.WatchQuery
			h.api.listWatchesFn = func(_ context.Context, q apiclient.WatchQuery) (apitypes.List[apitypes.Watch], error) {
				got = q
				return apitypes.List[apitypes.Watch]{}, nil
			}
			h.mustRun(tc.args...)
			tc.check(t, got)
		})
	}
}

// TestWatchEventsLimitReachesTheQuery guards the limit flag.
func TestWatchEventsLimitReachesTheQuery(t *testing.T) {
	h := newCLIHarness(t)
	var gotLimit int
	h.api.watchEventsFn = func(_ context.Context, _ string, limit int) (apitypes.List[apitypes.Event], error) {
		gotLimit = limit
		return apitypes.List[apitypes.Event]{}, nil
	}
	h.mustRun("watch", "events", "w_1", "--limit", "5")
	if gotLimit != 5 {
		t.Errorf("limit = %d, want 5", gotLimit)
	}
}

// TestNotificationsLimitReachesTheQuery guards the limit flag.
func TestNotificationsLimitReachesTheQuery(t *testing.T) {
	h := newCLIHarness(t)
	var gotLimit int
	h.api.notificationsFn = func(_ context.Context, limit int) (apitypes.List[apitypes.Notification], error) {
		gotLimit = limit
		return apitypes.List[apitypes.Notification]{}, nil
	}
	h.mustRun("notifications", "--limit", "9")
	if gotLimit != 9 {
		t.Errorf("limit = %d, want 9", gotLimit)
	}
}

// TestWatchChannelsSendsEveryName guards the variadic argument list, which is
// easy to truncate when building the request.
func TestWatchChannelsSendsEveryName(t *testing.T) {
	h := newCLIHarness(t)
	var got []string
	h.api.updateWatchFn = func(_ context.Context, id string, req apitypes.UpdateWatchRequest) (apitypes.Watch, error) {
		if req.Channels != nil {
			got = *req.Channels
		}
		return apitypes.Watch{ID: id, Channels: got}, nil
	}

	h.mustRun("watch", "channels", "w_1", "one", "two", "three")
	if len(got) != 3 || got[0] != "one" || got[2] != "three" {
		t.Errorf("channels = %v, want all three names in order", got)
	}
}

// TestCredsSetValidatesBeforeSending guards the validation order: a bad
// invocation must not reach the API.
func TestCredsSetValidatesBeforeSending(t *testing.T) {
	h := newCLIHarness(t)
	var gotHost string
	h.api.putCredsFn = func(_ context.Context, host string, req apitypes.CredentialsRequest) (apitypes.CredentialsInfo, error) {
		gotHost = host
		return apitypes.CredentialsInfo{Host: host, Username: req.Username, Kind: req.Kind}, nil
	}

	// --password-stdin with nothing on stdin must fail before any request.
	err := h.run("creds", "set", "ghcr.io", "--username", "u", "--kind", "token", "--password-stdin")
	if err == nil {
		t.Fatal("expected an error with no password on stdin")
	}
	if gotHost != "" {
		t.Error("a request was sent despite the missing password")
	}
	if !strings.Contains(err.Error(), "stdin") {
		t.Errorf("error = %v, want it to mention stdin", err)
	}

	// An invalid kind must also be rejected locally.
	if err := h.run("creds", "set", "ghcr.io", "--kind", "voodoo"); err == nil {
		t.Error("expected an error for an unknown credential kind")
	}
	if gotHost != "" {
		t.Error("a request was sent despite the invalid kind")
	}
}

// TestWatchAddSendsEveryTagAndPattern guards the request the server turns into
// one watch per ref.
func TestWatchAddSendsEveryTagAndPattern(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.CreateWatchRequest
	h.api.createWatchFn = func(_ context.Context, req apitypes.CreateWatchRequest) (apitypes.CreateWatchResponse, error) {
		got = req
		return apitypes.CreateWatchResponse{Image: req.Image}, nil
	}

	h.mustRun("watch", "add", "ghcr.io/owner/app",
		"--tag", "v1", "--tag", "v2",
		"--channel", "ops", "--channel", "oncall",
		"--disabled", "--no-notify-on-failure")

	if len(got.Tags) != 2 || got.Tags[0] != "v1" || got.Tags[1] != "v2" {
		t.Errorf("Tags = %v, want [v1 v2]", got.Tags)
	}
	if len(got.Channels) != 2 {
		t.Errorf("Channels = %v, want two names", got.Channels)
	}
	if !got.Disabled {
		t.Error("Disabled = false for --disabled")
	}
	if got.NotifyOnFailure == nil || *got.NotifyOnFailure {
		t.Error("NotifyOnFailure is not explicitly false for --no-notify-on-failure")
	}
}

// TestChannelAddEmailSendsEveryRecipient guards the repeatable --to flag.
func TestChannelAddEmailSendsEveryRecipient(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.CreateChannelRequest
	h.api.createChannelFn = func(_ context.Context, req apitypes.CreateChannelRequest) (apitypes.Channel, error) {
		got = req
		return apitypes.Channel{ID: "c_1", Name: req.Name, Type: req.Type}, nil
	}

	h.mustRun("channel", "add", "email", "--name", "mail",
		"--smtp-host", "smtp.example.com", "--from", "a@b.c",
		"--to", "one@example.com", "--to", "two@example.com", "--to", "three@example.com")

	if got.Config[apitypes.ConfigTo] != "one@example.com,two@example.com,three@example.com" {
		t.Errorf("to = %q, want all three recipients", got.Config[apitypes.ConfigTo])
	}
}

// TestJSONOutputIsParseable guards the scripting contract: -o json must emit
// valid JSON with no extra chatter on stdout.
func TestJSONOutputIsParseable(t *testing.T) {
	h := newCLIHarness(t)
	h.api.listWatchesFn = func(context.Context, apiclient.WatchQuery) (apitypes.List[apitypes.Watch], error) {
		return apitypes.NewList([]apitypes.Watch{
			{ID: "w_1", Image: "ghcr.io/owner/app:v1", Enabled: true},
		}, 1, 50, 0), nil
	}

	out := h.mustRun("watch", "list", "-o", "json")
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("json output does not start with an object:\n%s", out)
	}
	for _, want := range []string{`"items"`, `"total": 1`, `"w_1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("json output is missing %q:\n%s", want, out)
		}
	}
}

// TestTableOutputGoesToStdoutAndPromptsToStderr guards the stream split, which
// is what makes `dit watch list > file` usable.
func TestTableOutputGoesToStdoutAndPromptsToStderr(t *testing.T) {
	h := newCLIHarness(t)
	h.api.listWatchesFn = func(context.Context, apiclient.WatchQuery) (apitypes.List[apitypes.Watch], error) {
		return apitypes.NewList([]apitypes.Watch{{ID: "w_1", Image: "img", Enabled: true}}, 1, 50, 0), nil
	}

	h.mustRun("watch", "list")
	if !strings.Contains(h.stdout.String(), "w_1") {
		t.Errorf("stdout does not carry the table:\n%s", h.stdout.String())
	}
	if strings.Contains(h.stderr.String(), "w_1") {
		t.Errorf("stderr carries the table, which would corrupt a redirect:\n%s", h.stderr.String())
	}
}

// TestDeleteRefusalsDoNotCallTheAPI guards that a declined confirmation is not
// silently turned into a deletion.
func TestDeleteRefusalsDoNotCallTheAPI(t *testing.T) {
	h := newCLIHarness(t)
	called := false
	h.api.deleteWatchFn = func(context.Context, string) error { called = true; return nil }
	h.api.deleteChannelFn = func(context.Context, string) error { called = true; return nil }
	h.api.deleteCredsFn = func(context.Context, string) error { called = true; return nil }

	for _, args := range [][]string{
		{"watch", "rm", "w_1"},
		{"channel", "rm", "c_1"},
		{"creds", "rm", "ghcr.io"},
	} {
		if err := h.run(args...); err == nil {
			t.Errorf("%v: expected an error without --yes", args)
		}
	}
	if called {
		t.Error("a deletion reached the API without confirmation")
	}
}

// TestUnknownFlagsAreRejected guards against a typo being silently ignored.
func TestUnknownFlagsAreRejected(t *testing.T) {
	h := newCLIHarness(t)
	for _, args := range [][]string{
		{"watch", "list", "--nonexistent"},
		{"watch", "add", "nginx", "--nope"},
		{"events", "--bogus"},
	} {
		if err := h.run(args...); err == nil {
			t.Errorf("%v: expected an error for an unknown flag", args)
		}
	}
}

// TestMissingRequiredArgumentIsRejected guards the argument count on the
// commands that take an ID.
func TestMissingRequiredArgumentIsRejected(t *testing.T) {
	h := newCLIHarness(t)
	for _, args := range [][]string{
		{"watch", "show"},
		{"watch", "check"},
		{"watch", "rm"},
		{"channel", "show"},
		{"channel", "test"},
		{"creds", "set"},
		{"watch", "add"},
	} {
		if err := h.run(args...); err == nil {
			t.Errorf("%v: expected an error for a missing argument", args)
		}
	}
}
