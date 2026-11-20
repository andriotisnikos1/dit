package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/check"
	"github.com/andriotisnikos1/dit/internal/config"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

// newReportHarness builds a server whose daily and weekly reports are enabled,
// over the same frozen clock and fakes as newHarness.
func newReportHarness(t *testing.T) *harness {
	t.Helper()
	db := testutil.Store(t)
	fake := registry.NewFake()
	builder := notify.NewFakeBuilder()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fixedNow := func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) }

	cfg := config.DefaultServer()
	cfg.APIToken = testToken
	cfg.CheckTimeout = config.Duration(5 * time.Second)
	cfg.ReportDailyAt = "08:00"
	cfg.ReportWeeklyAt = "09:30"
	cfg.ReportWeeklyDay = "mon"

	schedule, err := cfg.ReportSchedule()
	if err != nil {
		t.Fatalf("ReportSchedule: %v", err)
	}
	engine, err := check.New(check.Options{
		Store:          db,
		Registry:       fake,
		Notifier:       builder,
		Logger:         logger,
		Interval:       time.Hour,
		Concurrency:    2,
		Timeout:        5 * time.Second,
		NotifyAttempts: 1,
		NotifyBackoff:  time.Millisecond,
		Now:            fixedNow,
		Schedule:       schedule,
	})
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	srv, err := New(Options{
		Config:   cfg,
		Store:    db,
		Engine:   engine,
		Registry: fake,
		Notifier: builder,
		Logger:   logger,
		Version:  "test",
		Now:      fixedNow,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	return &harness{t: t, server: srv, store: db, registry: fake, notifier: builder, handler: srv.Handler()}
}

func TestFlagListingProjectsReportingFlags(t *testing.T) {
	h := newHarness(t)

	for name, value := range map[string]any{
		"notify_individual": false,
		"report_daily":      true,
	} {
		if rec := h.authed(http.MethodPut, "/api/v1/flags/"+name,
			apitypes.SetFlagRequest{Value: value}); rec.Code != http.StatusOK {
			t.Fatalf("set global %s: status %d, body %s", name, rec.Code, rec.Body.String())
		}
	}
	created := h.createWatch("nginx:1.27", apitypes.CreateWatchRequest{Tags: []string{"1.27"}})
	watch := created.Watches[0]

	rec := h.authed(http.MethodGet, "/api/v1/watches/"+watch.ID+"/flags", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var list apitypes.FlagList
	h.decode(rec, &list)

	effective := map[string]any{}
	global := map[string]any{}
	for _, f := range list {
		if f.Effective != nil {
			effective[f.Name] = *f.Effective
		}
		if f.Global != nil {
			global[f.Name] = *f.Global
		}
	}
	// A flag missing from the projection silently reports its default, which
	// is how these flags were first shipped broken.
	if got, ok := effective["notify_individual"]; !ok || got != false {
		t.Errorf("effective notify_individual = %v (present=%v), want false", got, ok)
	}
	if got, ok := effective["report_daily"]; !ok || got != true {
		t.Errorf("effective report_daily = %v (present=%v), want true", got, ok)
	}
	if got, ok := effective["report_weekly"]; !ok || got != false {
		t.Errorf("effective report_weekly = %v (present=%v), want false", got, ok)
	}
	if got := global["notify_individual"]; got != false {
		t.Errorf("global notify_individual = %v, want false", got)
	}
}

func TestReportStatusEndpoint(t *testing.T) {
	h := newReportHarness(t)

	rec := h.authed(http.MethodGet, "/api/v1/reports", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var status apitypes.ReportStatus
	h.decode(rec, &status)
	if status.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", status.Timezone)
	}
	if len(status.Reports) != 2 {
		t.Fatalf("reports = %d, want 2", len(status.Reports))
	}
	if !status.Reports[0].Enabled || !status.Reports[1].Enabled {
		t.Errorf("both reports should be enabled in this harness: %+v", status.Reports)
	}
	wantNext := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	if status.Reports[0].NextRunAt == nil || !status.Reports[0].NextRunAt.Equal(wantNext) {
		t.Errorf("daily next = %v, want %v", status.Reports[0].NextRunAt, wantNext)
	}
	if status.Reports[0].Schedule != "daily at 08:00 (UTC)" {
		t.Errorf("daily schedule = %q", status.Reports[0].Schedule)
	}
	if status.Reports[1].Schedule != "Mondays at 09:30 (UTC)" {
		t.Errorf("weekly schedule = %q", status.Reports[1].Schedule)
	}

	// Unauthenticated access stays blocked.
	if unauthed := h.do(http.MethodGet, "/api/v1/reports", nil, "\x00none"); unauthed.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", unauthed.Code)
	}
}

// reportFixture creates a watch, a default channel, a baseline and one muted
// digest change, returning the watch ID and the channel's recorder.
func reportFixture(t *testing.T, h *harness) (string, *notify.Recording) {
	t.Helper()
	ctx := context.Background()

	rec := h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name:   "ops",
		Type:   apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "dit-test"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create channel: status %d, body %s", rec.Code, rec.Body.String())
	}
	var channel apitypes.Channel
	h.decode(rec, &channel)
	yes := true
	if patch := h.authed(http.MethodPatch, "/api/v1/channels/"+channel.ID,
		apitypes.UpdateChannelRequest{IsDefault: &yes}); patch.Code != http.StatusOK {
		t.Fatalf("make channel default: status %d, body %s", patch.Code, patch.Body.String())
	}

	created := h.createWatch("nginx:1.27", apitypes.CreateWatchRequest{Tags: []string{"1.27"}})
	watch := created.Watches[0]

	for name, value := range map[string]any{
		"notify_individual": false,
		"report_daily":      true,
	} {
		rec := h.authed(http.MethodPut, "/api/v1/watches/"+watch.ID+"/flags/"+name,
			apitypes.SetFlagRequest{Value: value})
		if rec.Code != http.StatusOK {
			t.Fatalf("set flag %s: status %d, body %s", name, rec.Code, rec.Body.String())
		}
	}

	h.registry.SetDigest(watch.Registry, watch.Repository, "1.27", testutil.Digest(1))
	if _, err := h.server.engine.CheckWatch(ctx, watch.ID, check.TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	h.registry.SetDigest(watch.Registry, watch.Repository, "1.27", testutil.Digest(2))
	if _, err := h.server.engine.CheckWatch(ctx, watch.ID, check.TriggerSchedule); err != nil {
		t.Fatalf("change check: %v", err)
	}

	builder := h.notifier
	transport, err := builder.Build(channel.ID, string(apitypes.ChannelNtfy), nil)
	if err != nil {
		t.Fatalf("build recording channel: %v", err)
	}
	recording, ok := transport.(*notify.Recording)
	if !ok {
		t.Fatalf("channel transport is %T, want *notify.Recording", transport)
	}
	return watch.ID, recording
}

func TestReportPreviewEndpoint(t *testing.T) {
	h := newReportHarness(t)
	_, rec := reportFixture(t, h)
	if rec.Count() != 0 {
		t.Fatalf("muted watch delivered %d individual notifications, want 0", rec.Count())
	}

	resp := h.authed(http.MethodGet, "/api/v1/reports/daily/preview", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.Code, resp.Body.String())
	}
	var result apitypes.ReportResult
	h.decode(resp, &result)
	if !result.DryRun {
		t.Error("DryRun = false on a preview")
	}
	if result.Events != 1 || result.Watches != 1 {
		t.Errorf("preview = %d events / %d watches, want 1/1", result.Events, result.Watches)
	}
	if result.Marked != 0 {
		t.Errorf("Marked = %d on a preview, want 0", result.Marked)
	}
	if result.Title == "" || result.Body == "" {
		t.Fatal("preview has an empty title or body")
	}
	if len(result.Channels) != 1 || result.Channels[0].Status != "preview" {
		t.Errorf("channels = %+v, want one preview entry", result.Channels)
	}
	if rec.Count() != 0 {
		t.Errorf("preview delivered %d messages, want 0", rec.Count())
	}
}

func TestReportSendEndpoint(t *testing.T) {
	h := newReportHarness(t)
	reportFixture(t, h)

	resp := h.authed(http.MethodPost, "/api/v1/reports/daily/send", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.Code, resp.Body.String())
	}
	var result apitypes.ReportResult
	h.decode(resp, &result)
	if result.Sent != 1 || result.Failed != 0 || result.Marked != 1 {
		t.Errorf("send = sent %d / failed %d / marked %d, want 1/0/1",
			result.Sent, result.Failed, result.Marked)
	}

	statusRec := h.authed(http.MethodGet, "/api/v1/reports", nil)
	var status apitypes.ReportStatus
	h.decode(statusRec, &status)
	if status.Reports[0].LastStatus != "sent" {
		t.Errorf("last status = %q, want sent", status.Reports[0].LastStatus)
	}
	if status.Reports[0].LastEvents != 1 {
		t.Errorf("last events = %d, want 1", status.Reports[0].LastEvents)
	}

	// Everything was delivered: a second run says nothing.
	again := h.authed(http.MethodPost, "/api/v1/reports/daily/send", nil)
	var empty apitypes.ReportResult
	h.decode(again, &empty)
	if empty.Events != 0 || empty.Sent != 0 {
		t.Errorf("second send = %d events / %d sent, want 0/0", empty.Events, empty.Sent)
	}
}

func TestReportUnknownKind(t *testing.T) {
	h := newReportHarness(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/reports/hourly/preview"},
		{http.MethodPost, "/api/v1/reports/hourly/send"},
	} {
		rec := h.authed(tc.method, tc.path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
			continue
		}
		if code := h.errorCode(rec); code != apitypes.CodeNotFound {
			t.Errorf("%s %s error code = %q", tc.method, tc.path, code)
		}
	}
}
