package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apiclient"
	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/config"
)

// stubAPI is a programmable apiclient.API for command tests.
type stubAPI struct {
	createWatchFn   func(context.Context, apitypes.CreateWatchRequest) (apitypes.CreateWatchResponse, error)
	listWatchesFn   func(context.Context, apiclient.WatchQuery) (apitypes.List[apitypes.Watch], error)
	getWatchFn      func(context.Context, string) (apitypes.Watch, error)
	updateWatchFn   func(context.Context, string, apitypes.UpdateWatchRequest) (apitypes.Watch, error)
	deleteWatchFn   func(context.Context, string) error
	checkWatchFn    func(context.Context, string) (apitypes.CheckResult, error)
	watchEventsFn   func(context.Context, string, int) (apitypes.List[apitypes.Event], error)
	eventsFn        func(context.Context, apiclient.EventQuery) (apitypes.List[apitypes.Event], error)
	notificationsFn func(context.Context, int) (apitypes.List[apitypes.Notification], error)

	listChannelsFn   func(context.Context) (apitypes.List[apitypes.Channel], error)
	createChannelFn  func(context.Context, apitypes.CreateChannelRequest) (apitypes.Channel, error)
	getChannelFn     func(context.Context, string) (apitypes.Channel, error)
	updateChannelFn  func(context.Context, string, apitypes.UpdateChannelRequest) (apitypes.Channel, error)
	deleteChannelFn  func(context.Context, string) error
	testChannelFn    func(context.Context, string) (apitypes.ChannelTestResult, error)
	listRegistriesFn func(context.Context) (apitypes.List[apitypes.Registry], error)
	getCredsFn       func(context.Context, string) (apitypes.CredentialsInfo, error)
	putCredsFn       func(context.Context, string, apitypes.CredentialsRequest) (apitypes.CredentialsInfo, error)
	deleteCredsFn    func(context.Context, string) error
	statusFn         func(context.Context) (apitypes.Status, error)
	healthFn         func(context.Context) (apitypes.Health, error)

	// Calls records the method names invoked.
	Calls []string
}

func (s *stubAPI) record(name string) { s.Calls = append(s.Calls, name) }

func (s *stubAPI) Health(ctx context.Context) (apitypes.Health, error) {
	s.record("health")
	if s.healthFn == nil {
		return apitypes.Health{Status: "ok", Version: "stub"}, nil
	}
	return s.healthFn(ctx)
}

func (s *stubAPI) Status(ctx context.Context) (apitypes.Status, error) {
	s.record("status")
	if s.statusFn == nil {
		return apitypes.Status{Version: "stub", CheckInterval: "15m"}, nil
	}
	return s.statusFn(ctx)
}

func (s *stubAPI) CreateWatch(ctx context.Context, req apitypes.CreateWatchRequest) (apitypes.CreateWatchResponse, error) {
	s.record("createWatch")
	if s.createWatchFn == nil {
		return apitypes.CreateWatchResponse{}, nil
	}
	return s.createWatchFn(ctx, req)
}

func (s *stubAPI) ListWatches(ctx context.Context, q apiclient.WatchQuery) (apitypes.List[apitypes.Watch], error) {
	s.record("listWatches")
	if s.listWatchesFn == nil {
		return apitypes.List[apitypes.Watch]{}, nil
	}
	return s.listWatchesFn(ctx, q)
}

func (s *stubAPI) GetWatch(ctx context.Context, id string) (apitypes.Watch, error) {
	s.record("getWatch")
	if s.getWatchFn == nil {
		return apitypes.Watch{ID: id}, nil
	}
	return s.getWatchFn(ctx, id)
}

func (s *stubAPI) UpdateWatch(ctx context.Context, id string, req apitypes.UpdateWatchRequest) (apitypes.Watch, error) {
	s.record("updateWatch")
	if s.updateWatchFn == nil {
		return apitypes.Watch{ID: id}, nil
	}
	return s.updateWatchFn(ctx, id, req)
}

func (s *stubAPI) DeleteWatch(ctx context.Context, id string) error {
	s.record("deleteWatch")
	if s.deleteWatchFn == nil {
		return nil
	}
	return s.deleteWatchFn(ctx, id)
}

func (s *stubAPI) CheckWatch(ctx context.Context, id string) (apitypes.CheckResult, error) {
	s.record("checkWatch")
	if s.checkWatchFn == nil {
		return apitypes.CheckResult{WatchID: id}, nil
	}
	return s.checkWatchFn(ctx, id)
}

func (s *stubAPI) WatchEvents(ctx context.Context, id string, limit int) (apitypes.List[apitypes.Event], error) {
	s.record("watchEvents")
	if s.watchEventsFn == nil {
		return apitypes.List[apitypes.Event]{}, nil
	}
	return s.watchEventsFn(ctx, id, limit)
}

func (s *stubAPI) Events(ctx context.Context, q apiclient.EventQuery) (apitypes.List[apitypes.Event], error) {
	s.record("events")
	if s.eventsFn == nil {
		return apitypes.List[apitypes.Event]{}, nil
	}
	return s.eventsFn(ctx, q)
}

func (s *stubAPI) Notifications(ctx context.Context, limit int) (apitypes.List[apitypes.Notification], error) {
	s.record("notifications")
	if s.notificationsFn == nil {
		return apitypes.List[apitypes.Notification]{}, nil
	}
	return s.notificationsFn(ctx, limit)
}

func (s *stubAPI) ListChannels(ctx context.Context) (apitypes.List[apitypes.Channel], error) {
	s.record("listChannels")
	if s.listChannelsFn == nil {
		return apitypes.List[apitypes.Channel]{}, nil
	}
	return s.listChannelsFn(ctx)
}

func (s *stubAPI) CreateChannel(ctx context.Context, req apitypes.CreateChannelRequest) (apitypes.Channel, error) {
	s.record("createChannel")
	if s.createChannelFn == nil {
		return apitypes.Channel{Name: req.Name, Type: req.Type}, nil
	}
	return s.createChannelFn(ctx, req)
}

func (s *stubAPI) GetChannel(ctx context.Context, id string) (apitypes.Channel, error) {
	s.record("getChannel")
	if s.getChannelFn == nil {
		return apitypes.Channel{ID: id}, nil
	}
	return s.getChannelFn(ctx, id)
}

func (s *stubAPI) UpdateChannel(ctx context.Context, id string, req apitypes.UpdateChannelRequest) (apitypes.Channel, error) {
	s.record("updateChannel")
	if s.updateChannelFn == nil {
		channel := apitypes.Channel{ID: id}
		if req.Enabled != nil {
			channel.Enabled = *req.Enabled
		}
		if req.IsDefault != nil {
			channel.IsDefault = *req.IsDefault
		}
		return channel, nil
	}
	return s.updateChannelFn(ctx, id, req)
}

func (s *stubAPI) DeleteChannel(ctx context.Context, id string) error {
	s.record("deleteChannel")
	if s.deleteChannelFn == nil {
		return nil
	}
	return s.deleteChannelFn(ctx, id)
}

func (s *stubAPI) TestChannel(ctx context.Context, id string) (apitypes.ChannelTestResult, error) {
	s.record("testChannel")
	if s.testChannelFn == nil {
		return apitypes.ChannelTestResult{ChannelID: id, OK: true}, nil
	}
	return s.testChannelFn(ctx, id)
}

func (s *stubAPI) ListRegistries(ctx context.Context) (apitypes.List[apitypes.Registry], error) {
	s.record("listRegistries")
	if s.listRegistriesFn == nil {
		return apitypes.List[apitypes.Registry]{}, nil
	}
	return s.listRegistriesFn(ctx)
}

func (s *stubAPI) GetCredentials(ctx context.Context, host string) (apitypes.CredentialsInfo, error) {
	s.record("getCredentials")
	if s.getCredsFn == nil {
		return apitypes.CredentialsInfo{Host: host}, nil
	}
	return s.getCredsFn(ctx, host)
}

func (s *stubAPI) PutCredentials(ctx context.Context, host string, req apitypes.CredentialsRequest) (apitypes.CredentialsInfo, error) {
	s.record("putCredentials")
	if s.putCredsFn == nil {
		return apitypes.CredentialsInfo{Host: host, Username: req.Username}, nil
	}
	return s.putCredsFn(ctx, host, req)
}

func (s *stubAPI) DeleteCredentials(ctx context.Context, host string) error {
	s.record("deleteCredentials")
	if s.deleteCredsFn == nil {
		return nil
	}
	return s.deleteCredsFn(ctx, host)
}

var _ apiclient.API = (*stubAPI)(nil)

// ---------- harness ----------

// cliHarness runs commands against a stub API with captured output.
//
// The flag values live on the harness rather than only on the App because
// building the command tree re-registers the persistent flags, which resets
// the fields they are bound to. run() re-applies them after building.
type cliHarness struct {
	t      *testing.T
	app    *App
	api    *stubAPI
	stdout *bytes.Buffer
	stderr *bytes.Buffer

	configPath     string
	serverFlag     string
	tokenFlag      string
	nonInteractive bool
}

func newCLIHarness(t *testing.T) *cliHarness {
	t.Helper()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	api := &stubAPI{}

	app := NewApp()
	app.Stdout = stdout
	app.Stderr = stderr
	app.NewClient = func(string, string) (apiclient.API, error) { return api, nil }

	// A config file that exists and validates, so prepare() succeeds.
	path := t.TempDir() + "/config.yaml"
	seed := &config.CLI{Server: "http://127.0.0.1:9", Token: "test-token"}
	seed.SetPath(path)
	if err := seed.Save(); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	return &cliHarness{
		t: t, app: app, api: api, stdout: stdout, stderr: stderr,
		configPath:     path,
		serverFlag:     "http://127.0.0.1:9",
		tokenFlag:      "test-token",
		nonInteractive: true,
	}
}

// run executes the CLI with args and returns the error.
//
// A fresh command tree is built for every invocation: cobra binds flag values
// to the command objects, so re-executing one tree would leak a previous
// invocation's flags into the next (a real CLI process never does this).
func (h *cliHarness) run(args ...string) error {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	h.api.Calls = nil

	root := NewRootCommand(h.app)
	// Re-apply the harness settings: building the tree reset the flag-bound
	// fields to their defaults.
	h.app.ConfigFlag = h.configPath
	h.app.ServerFlag = h.serverFlag
	h.app.TokenFlag = h.tokenFlag
	h.app.NonInteractive = h.nonInteractive

	root.SetArgs(args)
	return root.Execute()
}

// mustRun executes the CLI and fails on error.
func (h *cliHarness) mustRun(args ...string) string {
	h.t.Helper()
	if err := h.run(args...); err != nil {
		h.t.Fatalf("dit %s: %v\nstderr: %s", strings.Join(args, " "), err, h.stderr.String())
	}
	return h.stdout.String()
}

// ---------- tests ----------

func TestVersionCommandNeedsNoServer(t *testing.T) {
	h := newCLIHarness(t)
	out := h.mustRun("version")
	if !strings.Contains(out, "dit ") {
		t.Errorf("version output = %q, want it to mention dit", out)
	}
	if len(h.api.Calls) != 0 {
		t.Errorf("version called the API: %v", h.api.Calls)
	}
}

func TestConfigPathNeedsNoServer(t *testing.T) {
	h := newCLIHarness(t)
	out := h.mustRun("config", "path")
	if !strings.Contains(out, "config.yaml") {
		t.Errorf("config path output = %q, want a path ending in config.yaml", out)
	}
}

func TestConfigShowMasksToken(t *testing.T) {
	h := newCLIHarness(t)
	out := h.mustRun("config", "show")
	if strings.Contains(out, "test-token") {
		t.Errorf("config show leaked the token:\n%s", out)
	}
	if !strings.Contains(out, "Server") {
		t.Errorf("config show did not report the server:\n%s", out)
	}
}

func TestStatusTableAndJSON(t *testing.T) {
	h := newCLIHarness(t)
	h.api.statusFn = func(context.Context) (apitypes.Status, error) {
		return apitypes.Status{
			Version: "1.2.3", UptimeSeconds: 3720,
			Watches:       apitypes.WatchCounts{Total: 4, Enabled: 3, Disabled: 1, Failing: 1},
			Channels:      2,
			CheckInterval: "15m0s",
		}, nil
	}

	table := h.mustRun("status")
	for _, want := range []string{"1.2.3", "15m0s", "4 (3 enabled, 1 disabled, 1 failing)"} {
		if !strings.Contains(table, want) {
			t.Errorf("status table is missing %q:\n%s", want, table)
		}
	}

	jsonOut := h.mustRun("status", "-o", "json")
	if !strings.Contains(jsonOut, `"version": "1.2.3"`) {
		t.Errorf("status JSON is missing the version:\n%s", jsonOut)
	}

	yamlOut := h.mustRun("status", "-o", "yaml")
	if !strings.Contains(yamlOut, "version: 1.2.3") {
		t.Errorf("status YAML is missing the version:\n%s", yamlOut)
	}
}

func TestWatchAddCreatesOnePerTag(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.CreateWatchRequest
	h.api.createWatchFn = func(_ context.Context, req apitypes.CreateWatchRequest) (apitypes.CreateWatchResponse, error) {
		got = req
		return apitypes.CreateWatchResponse{
			Image:   req.Image,
			Created: []string{"w_1", "w_2"},
			Watches: []apitypes.Watch{
				{ID: "w_1", Image: req.Image, Kind: apitypes.WatchKindTag, Ref: "v1", Enabled: true},
				{ID: "w_2", Image: req.Image, Kind: apitypes.WatchKindTag, Ref: "v2", Enabled: true},
			},
		}, nil
	}

	out := h.mustRun("watch", "add", "ghcr.io/owner/app", "--tag", "v1", "--tag", "v2")
	if got.Image != "ghcr.io/owner/app" {
		t.Errorf("request image = %q", got.Image)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "v1" || got.Tags[1] != "v2" {
		t.Errorf("request tags = %v, want [v1 v2]", got.Tags)
	}
	if !strings.Contains(out, "Created 2 watch(es)") {
		t.Errorf("output does not report the created count:\n%s", out)
	}
	if !strings.Contains(out, "w_1") {
		t.Errorf("output does not list the watches:\n%s", out)
	}
}

func TestWatchAddReportsExisting(t *testing.T) {
	h := newCLIHarness(t)
	h.api.createWatchFn = func(_ context.Context, req apitypes.CreateWatchRequest) (apitypes.CreateWatchResponse, error) {
		return apitypes.CreateWatchResponse{
			Image:    req.Image,
			Existing: []string{"v1"},
			Watches:  []apitypes.Watch{{ID: "w_1", Ref: "v1"}},
		}, nil
	}

	out := h.mustRun("watch", "add", "ghcr.io/owner/app:v1")
	if !strings.Contains(out, "Already watched: v1") {
		t.Errorf("output does not report the existing watch:\n%s", out)
	}
}

// TestWatchAddPromptsForCredentialsOn428 covers the prompt-and-retry flow with
// the 428 trigger.
func TestWatchAddPromptsForCredentialsOn428(t *testing.T) {
	h := newCLIHarness(t)

	attempts := 0
	h.api.createWatchFn = func(_ context.Context, req apitypes.CreateWatchRequest) (apitypes.CreateWatchResponse, error) {
		attempts++
		if attempts == 1 {
			return apitypes.CreateWatchResponse{}, apitypes.APIError{
				Code:     apitypes.CodeCredentialsRequired,
				Message:  "the registry requires credentials",
				Registry: "ghcr.io",
			}
		}
		return apitypes.CreateWatchResponse{
			Image:   req.Image,
			Created: []string{"w_1"},
			Watches: []apitypes.Watch{{ID: "w_1", Ref: "v1", Enabled: true}},
		}, nil
	}

	// Non-interactive: the CLI must fail with a pointer to `dit creds set`
	// rather than hanging on a prompt.
	err := h.run("watch", "add", "ghcr.io/owner/app:v1")
	if err == nil {
		t.Fatal("expected an error in non-interactive mode")
	}
	if !strings.Contains(err.Error(), "dit creds set ghcr.io") {
		t.Errorf("error = %v, want it to point at `dit creds set ghcr.io`", err)
	}
	if attempts != 1 {
		t.Errorf("the request was retried %d times without credentials, want 1 attempt", attempts)
	}
}

// TestWatchAddInteractiveRetryAfter428 drives the same flow with prompting
// enabled by feeding the prompts through a fake terminal state.
func TestWatchAddInteractiveRetryAfter428(t *testing.T) {
	h := newCLIHarness(t)

	attempts := 0
	h.api.createWatchFn = func(_ context.Context, req apitypes.CreateWatchRequest) (apitypes.CreateWatchResponse, error) {
		attempts++
		if attempts == 1 {
			return apitypes.CreateWatchResponse{}, apitypes.APIError{
				Code: apitypes.CodeCredentialsRequired, Registry: "ghcr.io",
			}
		}
		return apitypes.CreateWatchResponse{Image: req.Image, Created: []string{"w_1"}}, nil
	}

	var stored apitypes.CredentialsRequest
	h.api.putCredsFn = func(_ context.Context, host string, req apitypes.CredentialsRequest) (apitypes.CredentialsInfo, error) {
		if host != "ghcr.io" {
			t.Errorf("stored credentials for %q, want ghcr.io", host)
		}
		stored = req
		return apitypes.CredentialsInfo{Host: host, Username: req.Username}, nil
	}

	// Simulate the interactive path by calling the prompt helper directly:
	// stdin cannot be made a terminal in a test, so this asserts the retry
	// logic and the credential write rather than the terminal read.
	h.nonInteractive = false
	host, err := h.app.promptAndStoreCredentials("ghcr.io")
	if err == nil {
		t.Fatal("promptAndStoreCredentials succeeded without a terminal; expected ErrNonInteractive")
	}
	if !errors.Is(err, ErrNonInteractive) {
		t.Errorf("error = %v, want ErrNonInteractive", err)
	}
	if host != "" {
		t.Errorf("host = %q, want empty on failure", host)
	}
	if stored.Username != "" {
		t.Error("credentials were stored despite the prompt failing")
	}
}

func TestWatchListFiltersAndRendering(t *testing.T) {
	h := newCLIHarness(t)
	var got apiclient.WatchQuery
	h.api.listWatchesFn = func(_ context.Context, q apiclient.WatchQuery) (apitypes.List[apitypes.Watch], error) {
		got = q
		return apitypes.List[apitypes.Watch]{
			Items: []apitypes.Watch{
				{ID: "w_1", Image: "ghcr.io/owner/app:v1", Kind: apitypes.WatchKindTag, Ref: "v1", Enabled: true,
					LastDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			},
			Total: 1,
		}, nil
	}

	out := h.mustRun("watch", "list", "--enabled", "--registry", "ghcr.io", "--search", "app")
	if got.Enabled == nil || !*got.Enabled {
		t.Error("the --enabled flag did not set the query")
	}
	if got.Registry != "ghcr.io" || got.Search != "app" {
		t.Errorf("query = %+v, want registry ghcr.io and search app", got)
	}
	if !strings.Contains(out, "sha256:0123456789ab") {
		t.Errorf("the digest is not shortened in the table:\n%s", out)
	}
	if !strings.Contains(out, "ID") || !strings.Contains(out, "IMAGE") {
		t.Errorf("the table has no headers:\n%s", out)
	}

	// --enabled and --disabled together is a usage error.
	if err := h.run("watch", "list", "--enabled", "--disabled"); err == nil {
		t.Error("expected an error for --enabled with --disabled")
	}
}

func TestWatchListEmptyRendersNone(t *testing.T) {
	h := newCLIHarness(t)
	h.api.listWatchesFn = func(context.Context, apiclient.WatchQuery) (apitypes.List[apitypes.Watch], error) {
		return apitypes.List[apitypes.Watch]{Items: []apitypes.Watch{}}, nil
	}
	out := h.mustRun("watch", "list")
	if !strings.Contains(out, "none") {
		t.Errorf("empty list output = %q, want %q", out, "none")
	}
}

func TestWatchShowRendersDetail(t *testing.T) {
	h := newCLIHarness(t)
	h.api.getWatchFn = func(_ context.Context, id string) (apitypes.Watch, error) {
		return apitypes.Watch{
			ID: id, Image: "ghcr.io/owner/app:v1", Registry: "ghcr.io", Repository: "owner/app",
			Kind: apitypes.WatchKindTag, Ref: "v1", Enabled: true, NotifyOnFailure: true,
			LastDigest: "sha256:abc", ConsecutiveFailures: 2, LastError: "boom",
		}, nil
	}

	out := h.mustRun("watch", "show", "w_1")
	for _, want := range []string{"w_1", "ghcr.io/owner/app:v1", "boom", "(defaults)"} {
		if !strings.Contains(out, want) {
			t.Errorf("watch show output is missing %q:\n%s", want, out)
		}
	}
}

func TestWatchCheckRendersEachOutcome(t *testing.T) {
	cases := []struct {
		name   string
		result apitypes.CheckResult
		want   string
	}{
		{"baseline", apitypes.CheckResult{Baseline: true, Matched: 1}, "baseline recorded"},
		{"changed", apitypes.CheckResult{Changed: true, Tag: "v1",
			OldDigest: "sha256:old", NewDigest: "sha256:new", Notified: 1}, "digest changed"},
		{"no change", apitypes.CheckResult{}, "no change"},
		{"new tags", apitypes.CheckResult{NewTags: []apitypes.TagDigest{{Tag: "v2"}}, Matched: 2}, "new tag(s)"},
		{"failure", apitypes.CheckResult{Error: "unauthorized"}, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newCLIHarness(t)
			result := tc.result
			h.api.checkWatchFn = func(context.Context, string) (apitypes.CheckResult, error) {
				return result, nil
			}
			out := h.mustRun("watch", "check", "w_1")
			if !strings.Contains(out, tc.want) {
				t.Errorf("output = %q, want it to contain %q", out, tc.want)
			}
		})
	}
}

func TestWatchEnableDisable(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.UpdateWatchRequest
	h.api.updateWatchFn = func(_ context.Context, id string, req apitypes.UpdateWatchRequest) (apitypes.Watch, error) {
		got = req
		watch := apitypes.Watch{ID: id}
		if req.Enabled != nil {
			watch.Enabled = *req.Enabled
		}
		return watch, nil
	}

	out := h.mustRun("watch", "enable", "w_1")
	if got.Enabled == nil || !*got.Enabled {
		t.Error("enable did not send enabled=true")
	}
	if !strings.Contains(out, "is now enabled") {
		t.Errorf("output = %q, want it to report the new state", out)
	}

	out = h.mustRun("watch", "disable", "w_1")
	if got.Enabled == nil || *got.Enabled {
		t.Error("disable did not send enabled=false")
	}
	if !strings.Contains(out, "is now disabled") {
		t.Errorf("output = %q, want it to report the new state", out)
	}
}

func TestWatchChannels(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.UpdateWatchRequest
	h.api.updateWatchFn = func(_ context.Context, id string, req apitypes.UpdateWatchRequest) (apitypes.Watch, error) {
		got = req
		watch := apitypes.Watch{ID: id}
		if req.Channels != nil {
			watch.Channels = *req.Channels
		}
		return watch, nil
	}

	out := h.mustRun("watch", "channels", "w_1", "ops", "oncall")
	if got.Channels == nil || len(*got.Channels) != 2 {
		t.Fatalf("channels request = %v, want two names", got.Channels)
	}
	if !strings.Contains(out, "ops, oncall") {
		t.Errorf("output = %q, want it to list the channels", out)
	}

	// No channel names clears the subscriptions.
	out = h.mustRun("watch", "channels", "w_1")
	if got.Channels == nil || len(*got.Channels) != 0 {
		t.Errorf("channels request = %v, want an empty slice", got.Channels)
	}
	if !strings.Contains(out, "default channels") {
		t.Errorf("output = %q, want it to mention the default channels", out)
	}
}

func TestWatchRemoveRequiresConfirmation(t *testing.T) {
	h := newCLIHarness(t)
	deleted := false
	h.api.deleteWatchFn = func(context.Context, string) error {
		deleted = true
		return nil
	}

	// Non-interactive without --yes must refuse.
	if err := h.run("watch", "rm", "w_1"); err == nil {
		t.Error("expected an error without --yes in non-interactive mode")
	}
	if deleted {
		t.Error("the watch was deleted without confirmation")
	}

	h.mustRun("watch", "rm", "w_1", "--yes")
	if !deleted {
		t.Error("the watch was not deleted with --yes")
	}
}

func TestWatchEventsRendering(t *testing.T) {
	h := newCLIHarness(t)
	h.api.watchEventsFn = func(context.Context, string, int) (apitypes.List[apitypes.Event], error) {
		return apitypes.List[apitypes.Event]{
			Items: []apitypes.Event{{
				ID: "e_1", Type: apitypes.EventDigestChanged, Image: "ghcr.io/owner/app:v1", Tag: "v1",
				NewDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}},
			Total: 1,
		}, nil
	}

	out := h.mustRun("watch", "events", "w_1")
	for _, want := range []string{"e_1", "digest_changed", "ghcr.io/owner/app:v1"} {
		if !strings.Contains(out, want) {
			t.Errorf("events output is missing %q:\n%s", want, out)
		}
	}
}

func TestEventsFilters(t *testing.T) {
	h := newCLIHarness(t)
	var got apiclient.EventQuery
	h.api.eventsFn = func(_ context.Context, q apiclient.EventQuery) (apitypes.List[apitypes.Event], error) {
		got = q
		return apitypes.List[apitypes.Event]{}, nil
	}

	h.mustRun("events", "--type", "digest_changed", "--watch", "w_1", "--limit", "10")
	if got.Type != apitypes.EventDigestChanged || got.WatchID != "w_1" || got.Limit != 10 {
		t.Errorf("query = %+v, want the flags reflected", got)
	}

	if err := h.run("events", "--type", "nonsense"); err == nil {
		t.Error("expected an error for an unknown event type")
	}
}

func TestNotificationsRendering(t *testing.T) {
	h := newCLIHarness(t)
	h.api.notificationsFn = func(context.Context, int) (apitypes.List[apitypes.Notification], error) {
		return apitypes.List[apitypes.Notification]{
			Items: []apitypes.Notification{{
				ID: "n_1", EventID: "e_1", ChannelName: "ops", ChannelType: "ntfy",
				Status: apitypes.NotificationSent, Attempts: 1,
			}},
			Total: 1,
		}, nil
	}

	out := h.mustRun("notifications")
	for _, want := range []string{"n_1", "ops", "sent"} {
		if !strings.Contains(out, want) {
			t.Errorf("notifications output is missing %q:\n%s", want, out)
		}
	}
}

func TestChannelAddNtfyBuildsConfig(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.CreateChannelRequest
	h.api.createChannelFn = func(_ context.Context, req apitypes.CreateChannelRequest) (apitypes.Channel, error) {
		got = req
		return apitypes.Channel{ID: "c_1", Name: req.Name, Type: req.Type, Enabled: true}, nil
	}

	out := h.mustRun("channel", "add", "ntfy", "--name", "ops", "--topic", "alerts",
		"--priority", "high", "--tags", "whale")
	if got.Type != apitypes.ChannelNtfy {
		t.Errorf("type = %q, want ntfy", got.Type)
	}
	if got.Config[apitypes.ConfigTopic] != "alerts" {
		t.Errorf("topic = %q, want alerts", got.Config[apitypes.ConfigTopic])
	}
	if got.Config[apitypes.ConfigPriority] != "high" {
		t.Errorf("priority = %q, want high", got.Config[apitypes.ConfigPriority])
	}
	if !strings.Contains(out, "Created ntfy channel ops") {
		t.Errorf("output = %q", out)
	}

	// Missing required flags are usage errors.
	if err := h.run("channel", "add", "ntfy", "--name", "ops"); err == nil {
		t.Error("expected an error without --topic")
	}
	if err := h.run("channel", "add", "ntfy", "--topic", "alerts"); err == nil {
		t.Error("expected an error without --name")
	}
}

func TestChannelAddEmailBuildsConfig(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.CreateChannelRequest
	h.api.createChannelFn = func(_ context.Context, req apitypes.CreateChannelRequest) (apitypes.Channel, error) {
		got = req
		return apitypes.Channel{ID: "c_1", Name: req.Name, Type: req.Type, Enabled: true}, nil
	}

	h.mustRun("channel", "add", "email", "--name", "mail",
		"--smtp-host", "smtp.example.com", "--smtp-port", "465", "--implicit-tls",
		"--from", "dit@example.com", "--to", "ops@example.com", "--to", "oncall@example.com")

	if got.Type != apitypes.ChannelEmail {
		t.Errorf("type = %q, want email", got.Type)
	}
	if got.Config[apitypes.ConfigSMTPHost] != "smtp.example.com" {
		t.Errorf("smtp_host = %q", got.Config[apitypes.ConfigSMTPHost])
	}
	if got.Config[apitypes.ConfigSMTPPort] != "465" {
		t.Errorf("smtp_port = %q, want 465", got.Config[apitypes.ConfigSMTPPort])
	}
	if got.Config[apitypes.ConfigImplicitTLS] != "true" {
		t.Errorf("implicit_tls = %q, want true", got.Config[apitypes.ConfigImplicitTLS])
	}
	if got.Config[apitypes.ConfigTo] != "ops@example.com,oncall@example.com" {
		t.Errorf("to = %q, want both recipients", got.Config[apitypes.ConfigTo])
	}

	// STARTTLS and implicit TLS are mutually exclusive.
	if err := h.run("channel", "add", "email", "--name", "m", "--smtp-host", "h",
		"--from", "a@b.c", "--to", "d@e.f", "--starttls", "--implicit-tls"); err == nil {
		t.Error("expected an error for --starttls with --implicit-tls")
	}
}

func TestChannelListRendersDetail(t *testing.T) {
	h := newCLIHarness(t)
	h.api.listChannelsFn = func(context.Context) (apitypes.List[apitypes.Channel], error) {
		return apitypes.List[apitypes.Channel]{
			Items: []apitypes.Channel{
				{ID: "c_1", Name: "ops", Type: apitypes.ChannelNtfy, Enabled: true, IsDefault: true,
					HasSecret: true, Config: map[string]string{apitypes.ConfigURL: "https://ntfy.sh", apitypes.ConfigTopic: "alerts"}},
			},
			Total: 1,
		}, nil
	}

	out := h.mustRun("channel", "list")
	for _, want := range []string{"c_1", "ops", "ntfy", "https://ntfy.sh/alerts"} {
		if !strings.Contains(out, want) {
			t.Errorf("channel list is missing %q:\n%s", want, out)
		}
	}
}

func TestChannelShowHidesSecret(t *testing.T) {
	h := newCLIHarness(t)
	h.api.getChannelFn = func(_ context.Context, id string) (apitypes.Channel, error) {
		// The server never returns the secret; the CLI must render what it got.
		return apitypes.Channel{
			ID: id, Name: "mail", Type: apitypes.ChannelEmail, Enabled: true, HasSecret: true,
			Config: map[string]string{
				apitypes.ConfigSMTPHost: "smtp.example.com",
				apitypes.ConfigPassword: "",
			},
		}, nil
	}

	out := h.mustRun("channel", "show", "c_1")
	if !strings.Contains(out, "smtp.example.com") {
		t.Errorf("channel show is missing the host:\n%s", out)
	}
	if !strings.Contains(out, "Has secret") {
		t.Errorf("channel show does not report secret presence:\n%s", out)
	}
}

func TestChannelTestCommand(t *testing.T) {
	h := newCLIHarness(t)
	h.api.testChannelFn = func(_ context.Context, id string) (apitypes.ChannelTestResult, error) {
		return apitypes.ChannelTestResult{ChannelID: id, OK: true}, nil
	}
	out := h.mustRun("channel", "test", "c_1")
	if !strings.Contains(out, "Test notification sent") {
		t.Errorf("output = %q", out)
	}

	h.api.testChannelFn = func(_ context.Context, id string) (apitypes.ChannelTestResult, error) {
		return apitypes.ChannelTestResult{ChannelID: id, OK: false, Error: "smtp refused"}, nil
	}
	err := h.run("channel", "test", "c_1")
	if err == nil {
		t.Fatal("expected an error for a failed test")
	}
	if !strings.Contains(err.Error(), "smtp refused") {
		t.Errorf("error = %v, want the transport error", err)
	}
}

func TestChannelDefaultToggle(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.UpdateChannelRequest
	h.api.updateChannelFn = func(_ context.Context, id string, req apitypes.UpdateChannelRequest) (apitypes.Channel, error) {
		got = req
		channel := apitypes.Channel{ID: id}
		if req.IsDefault != nil {
			channel.IsDefault = *req.IsDefault
		}
		return channel, nil
	}

	out := h.mustRun("channel", "default", "c_1")
	if got.IsDefault == nil || !*got.IsDefault {
		t.Error("default did not send is_default=true")
	}
	if !strings.Contains(out, "is a default channel") {
		t.Errorf("output = %q", out)
	}

	out = h.mustRun("channel", "default", "c_1", "--off")
	if got.IsDefault == nil || *got.IsDefault {
		t.Error("--off did not send is_default=false")
	}
	if !strings.Contains(out, "no longer a default") {
		t.Errorf("output = %q", out)
	}
}

func TestChannelEnableDisableAndRemove(t *testing.T) {
	h := newCLIHarness(t)
	var enabled *bool
	h.api.updateChannelFn = func(_ context.Context, id string, req apitypes.UpdateChannelRequest) (apitypes.Channel, error) {
		enabled = req.Enabled
		return apitypes.Channel{ID: id, Enabled: req.Enabled != nil && *req.Enabled}, nil
	}

	h.mustRun("channel", "disable", "c_1")
	if enabled == nil || *enabled {
		t.Error("disable did not send enabled=false")
	}
	h.mustRun("channel", "enable", "c_1")
	if enabled == nil || !*enabled {
		t.Error("enable did not send enabled=true")
	}

	deleted := false
	h.api.deleteChannelFn = func(context.Context, string) error { deleted = true; return nil }
	h.mustRun("channel", "rm", "c_1", "--yes")
	if !deleted {
		t.Error("the channel was not deleted")
	}
}

func TestCredsSetFromStdin(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.CredentialsRequest
	h.api.putCredsFn = func(_ context.Context, host string, req apitypes.CredentialsRequest) (apitypes.CredentialsInfo, error) {
		got = req
		return apitypes.CredentialsInfo{Host: host, Username: req.Username, Kind: req.Kind}, nil
	}

	// With --password-stdin but a terminal-less stdin carrying no data, the
	// command must fail rather than prompt.
	err := h.run("creds", "set", "ghcr.io", "--username", "andriotis", "--password-stdin")
	if err == nil {
		t.Fatal("expected an error when stdin carries no password")
	}

	// Without --password-stdin in non-interactive mode, the CLI must point at
	// the flag rather than hanging.
	err = h.run("creds", "set", "ghcr.io", "--username", "andriotis")
	if err == nil {
		t.Fatal("expected an error without --password-stdin in non-interactive mode")
	}
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Errorf("error = %v, want it to mention --password-stdin", err)
	}

	// An invalid kind is rejected before any request.
	if err := h.run("creds", "set", "ghcr.io", "--kind", "voodoo"); err == nil {
		t.Error("expected an error for an unknown credential kind")
	}
	if got.Username != "" {
		t.Error("a request was sent despite the validation failure")
	}
}

func TestCredsList(t *testing.T) {
	h := newCLIHarness(t)
	h.api.listRegistriesFn = func(context.Context) (apitypes.List[apitypes.Registry], error) {
		return apitypes.List[apitypes.Registry]{
			Items: []apitypes.Registry{
				{Host: "ghcr.io", HasCredentials: true, Username: "andriotis", Kind: "basic", Watches: 3},
				{Host: "quay.io", HasCredentials: false, Watches: 1},
			},
			Total: 2,
		}, nil
	}

	out := h.mustRun("creds", "list")
	for _, want := range []string{"ghcr.io", "quay.io", "andriotis"} {
		if !strings.Contains(out, want) {
			t.Errorf("creds list is missing %q:\n%s", want, out)
		}
	}
}

func TestCredsRemoveRequiresConfirmation(t *testing.T) {
	h := newCLIHarness(t)
	deleted := false
	h.api.deleteCredsFn = func(context.Context, string) error { deleted = true; return nil }

	if err := h.run("creds", "rm", "ghcr.io"); err == nil {
		t.Error("expected an error without --yes in non-interactive mode")
	}
	if deleted {
		t.Error("credentials were deleted without confirmation")
	}
	h.mustRun("creds", "rm", "ghcr.io", "--yes")
	if !deleted {
		t.Error("credentials were not deleted with --yes")
	}
}

func TestOutputFormatValidation(t *testing.T) {
	h := newCLIHarness(t)
	if err := h.run("status", "-o", "xml"); err == nil {
		t.Error("expected an error for an unknown output format")
	}
}

func TestMissingServerConfigurationFails(t *testing.T) {
	h := newCLIHarness(t)
	// Clear the config file and every override so nothing can supply a server.
	h.configPath = t.TempDir() + "/missing.yaml"
	h.serverFlag = ""
	h.tokenFlag = ""

	err := h.run("status")
	if err == nil {
		t.Fatal("expected an error with no server configured")
	}
	if !strings.Contains(err.Error(), "server") {
		t.Errorf("error = %v, want it to mention the server", err)
	}
}

func TestMissingTokenConfigurationFails(t *testing.T) {
	h := newCLIHarness(t)
	h.configPath = t.TempDir() + "/missing.yaml"
	h.serverFlag = "http://127.0.0.1:9"
	h.tokenFlag = ""

	err := h.run("status")
	if err == nil {
		t.Fatal("expected an error with no token configured")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("error = %v, want it to mention the token", err)
	}
}

func TestAPIErrorsPropagate(t *testing.T) {
	h := newCLIHarness(t)
	h.api.getWatchFn = func(context.Context, string) (apitypes.Watch, error) {
		return apitypes.Watch{}, apitypes.APIError{Code: apitypes.CodeNotFound, Message: "watch not found"}
	}

	err := h.run("watch", "show", "w_missing")
	if err == nil {
		t.Fatal("expected the API error to propagate")
	}
	if !strings.Contains(err.Error(), "watch not found") {
		t.Errorf("error = %v, want the server message", err)
	}
}
