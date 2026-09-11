package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/andriotisnikos1/dit/internal/apiclient"
	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func (a *App) newEventsCommand() *cobra.Command {
	var (
		limit int
		watch string
		kind  string
	)
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show the global event feed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eventType := apitypes.EventType(kind)
			if eventType != "" && !eventType.Valid() {
				return fmt.Errorf("--type must be one of %s", eventTypeNames())
			}

			ctx, cancel := a.Context()
			defer cancel()

			list, err := a.Client.Events(ctx, apiclient.EventQuery{
				WatchID: watch,
				Type:    eventType,
				Limit:   limit,
			})
			if err != nil {
				return err
			}
			return a.Out.Print(list, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(list)
				}
				return writeEventTable(o, list.Items)
			})
		},
	}
	flags := cmd.Flags()
	flags.IntVar(&limit, "limit", 50, "maximum events to show")
	flags.StringVar(&watch, "watch", "", "only events for this watch ID")
	flags.StringVar(&kind, "type", "", "only events of this type ("+eventTypeNames()+")")
	return cmd
}

func (a *App) newNotificationsCommand() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "notifications",
		Short: "Show the notification delivery log",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			list, err := a.Client.Notifications(ctx, limit)
			if err != nil {
				return err
			}
			return a.Out.Print(list, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(list)
				}
				return writeNotificationTable(o, list.Items)
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum rows to show")
	cmd.AddCommand(a.newNotificationsRetryCommand())
	return cmd
}

func (a *App) newNotificationsRetryCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "retry <id>",
		Short: "Re-deliver a failed notification",
		Long: `Re-send a recorded notification through its channel without re-running the
check. A notification that was already delivered is not retried.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			result, err := a.Client.RetryNotification(ctx, args[0])
			if err != nil {
				return err
			}
			return a.Out.Print(result, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(result)
				}
				if !result.OK {
					return fmt.Errorf("retry failed: %s", result.Error)
				}
				return o.Println("Re-delivered %s to %s (attempts: %d)",
					result.Notification.ID, result.Notification.ChannelName, result.Notification.Attempts)
			})
		},
	}
}

func writeNotificationTable(o *Output, notifications []apitypes.Notification) error {
	rows := make([][]string, 0, len(notifications))
	for _, n := range notifications {
		rows = append(rows, []string{
			n.ID,
			formatTime(&n.CreatedAt),
			n.EventID,
			n.ChannelName,
			n.ChannelType,
			string(n.Status),
			fmt.Sprintf("%d", n.Attempts),
			orDash(n.LastError),
		})
	}
	return o.Table([]string{"ID", "WHEN", "EVENT", "CHANNEL", "TYPE", "STATUS", "TRIES", "ERROR"}, rows)
}

func eventTypeNames() string {
	types := apitypes.EventTypes()
	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, string(t))
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
