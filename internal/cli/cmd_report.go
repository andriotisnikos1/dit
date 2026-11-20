package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func (a *App) newReportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Inspect, preview and send the daily and weekly reports",
		Long: `Reports summarise update events instead of notifying one by one.

Set notify_individual=false to silence per-update notifications, then enable
report_daily and/or report_weekly on the watches you want summarised. The
schedule lives on the server (DIT_REPORT_DAILY_AT, DIT_REPORT_WEEKLY_AT,
DIT_REPORT_WEEKLY_DAY, DIT_REPORT_TIMEZONE); a period with no changes sends
nothing.`,
	}
	cmd.AddCommand(
		a.newReportStatusCommand(),
		a.newReportPreviewCommand(),
		a.newReportSendCommand(),
	)
	return cmd
}

func (a *App) newReportStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the report schedule and the last run of each kind",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.Context()
			defer cancel()
			status, err := a.Client.Reports(ctx)
			if err != nil {
				return err
			}
			return a.Out.Print(status, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(status)
				}
				headers := []string{"KIND", "ENABLED", "SCHEDULE", "NEXT RUN", "LAST RUN", "STATUS", "EVENTS", "WATCHES"}
				rows := make([][]string, 0, len(status.Reports))
				for _, r := range status.Reports {
					rows = append(rows, []string{
						string(r.Kind),
						fmt.Sprintf("%t", r.Enabled),
						valueOrDash(r.Schedule),
						timeOrDash(r.NextRunAt),
						timeOrDash(r.LastRunAt),
						valueOrDash(r.LastStatus),
						fmt.Sprintf("%d", r.LastEvents),
						fmt.Sprintf("%d", r.LastWatches),
					})
				}
				return o.Table(headers, rows)
			})
		},
	}
}

func (a *App) newReportPreviewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "preview <daily|weekly>",
		Short: "Render the next report without sending it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := apitypes.ReportKind(args[0])
			if !kind.Valid() {
				return fmt.Errorf("unknown report kind %q; valid: daily, weekly", args[0])
			}
			ctx, cancel := a.Context()
			defer cancel()
			result, err := a.Client.PreviewReport(ctx, kind)
			if err != nil {
				return err
			}
			return a.Out.Print(result, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(result)
				}
				if result.Events == 0 {
					return o.Println("Nothing to report: no unreported changes.")
				}
				if err := o.Println("%s", result.Title); err != nil {
					return err
				}
				if err := o.Println(""); err != nil {
					return err
				}
				if err := o.Println("%s", result.Body); err != nil {
					return err
				}
				return o.Println("\nWould send to %d channel(s).", len(result.Channels))
			})
		},
	}
}

func (a *App) newReportSendCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "send <daily|weekly>",
		Short: "Send a report now",
		Long: `Send a report now, exactly as the scheduler would. Events delivered by the
report are marked as reported, so the next scheduled run will not repeat them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := apitypes.ReportKind(args[0])
			if !kind.Valid() {
				return fmt.Errorf("unknown report kind %q; valid: daily, weekly", args[0])
			}
			ctx, cancel := a.Context()
			defer cancel()
			result, err := a.Client.SendReport(ctx, kind)
			if err != nil {
				return err
			}
			return a.Out.Print(result, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(result)
				}
				if result.Events == 0 {
					return o.Println("Nothing to report: no unreported changes.")
				}
				lines := []string{fmt.Sprintf(
					"%s report: %d %s across %d %s, delivered to %d/%d channel(s).",
					kind, result.Events, pluralWord(result.Events, "change", "changes"),
					result.Watches, pluralWord(result.Watches, "image", "images"),
					result.Sent, result.Sent+result.Failed)}
				for _, ch := range result.Channels {
					if ch.Status == "failed" {
						lines = append(lines, fmt.Sprintf("  %s (%s): %s", ch.ChannelName, ch.ChannelType, ch.Error))
					}
				}
				if result.Failed > 0 {
					lines = append(lines,
						"Failed deliveries leave their events pending for the next run.")
				}
				return o.Println("%s", joinLines(lines))
			})
		},
	}
}

func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func timeOrDash(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format("2006-01-02 15:04 MST")
}

func pluralWord(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func joinLines(lines []string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}
