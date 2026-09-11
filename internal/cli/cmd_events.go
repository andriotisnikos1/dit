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
				rows := make([][]string, 0, len(list.Items))
				for _, n := range list.Items {
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
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum rows to show")
	return cmd
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
