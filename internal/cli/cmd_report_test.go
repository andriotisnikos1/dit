package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func TestReportStatusCommand(t *testing.T) {
	h := newCLIHarness(t)
	next := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	last := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)

	h.api.reportsFn = func(context.Context) (apitypes.ReportStatus, error) {
		return apitypes.ReportStatus{
			Timezone: "UTC",
			Reports: []apitypes.ReportInfo{
				{
					Kind:        apitypes.ReportDaily,
					Enabled:     true,
					Schedule:    "daily at 08:00 (UTC)",
					NextRunAt:   &next,
					LastRunAt:   &last,
					LastStatus:  "sent",
					LastEvents:  12,
					LastWatches: 4,
				},
				{Kind: apitypes.ReportWeekly, Enabled: false},
			},
		}, nil
	}

	out := h.mustRun("report", "status")
	for _, want := range []string{"daily", "daily at 08:00 (UTC)", "sent", "12", "weekly", "false"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output does not contain %q:\n%s", want, out)
		}
	}
	if len(h.api.Calls) != 1 || h.api.Calls[0] != "reports" {
		t.Errorf("API calls = %v, want [reports]", h.api.Calls)
	}
}

func TestReportPreviewCommand(t *testing.T) {
	h := newCLIHarness(t)
	var gotKind apitypes.ReportKind
	h.api.previewReportFn = func(_ context.Context, kind apitypes.ReportKind) (apitypes.ReportResult, error) {
		gotKind = kind
		return apitypes.ReportResult{
			Kind: kind, DryRun: true,
			Title:  "dit daily report: 1 change in nginx:1.27",
			Body:   "dit daily report\nPeriod: ...\n\nnginx:1.27  (w_1)\n  2026-09-11 10:00 UTC  digest changed",
			Events: 1, Watches: 1,
			Channels: []apitypes.ReportChannelResult{{
				ChannelID: "c_1", ChannelName: "ops", ChannelType: "ntfy", Status: "preview", Events: 1,
			}},
		}, nil
	}

	out := h.mustRun("report", "preview", "daily")
	if gotKind != apitypes.ReportDaily {
		t.Errorf("kind = %q, want daily", gotKind)
	}
	for _, want := range []string{"dit daily report: 1 change in nginx:1.27", "digest changed", "Would send to 1 channel(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview output does not contain %q:\n%s", want, out)
		}
	}
}

func TestReportCommandsRejectUnknownKind(t *testing.T) {
	h := newCLIHarness(t)
	for _, args := range [][]string{
		{"report", "preview", "hourly"},
		{"report", "send", "hourly"},
	} {
		if err := h.run(args...); err == nil {
			t.Errorf("dit %s accepted an unknown kind", strings.Join(args, " "))
		}
		if len(h.api.Calls) != 0 {
			t.Errorf("dit %s called the API before validation: %v", strings.Join(args, " "), h.api.Calls)
		}
	}
}

func TestReportSendCommandReportsPartialFailure(t *testing.T) {
	h := newCLIHarness(t)
	h.api.sendReportFn = func(_ context.Context, kind apitypes.ReportKind) (apitypes.ReportResult, error) {
		return apitypes.ReportResult{
			Kind: kind, Events: 3, Watches: 2, Marked: 2, Sent: 1, Failed: 1,
			Channels: []apitypes.ReportChannelResult{
				{ChannelName: "ops", ChannelType: "ntfy", Status: "sent"},
				{ChannelName: "mail", ChannelType: "email", Status: "failed", Error: "smtp 550"},
			},
		}, nil
	}

	out := h.mustRun("report", "send", "daily")
	for _, want := range []string{
		"daily report: 3 changes across 2 images, delivered to 1/2 channel(s)",
		"mail (email): smtp 550",
		"leave their events pending",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("send output does not contain %q:\n%s", want, out)
		}
	}
}
