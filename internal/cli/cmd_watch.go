package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func (a *App) newWatchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Manage image watches",
	}
	cmd.AddCommand(
		a.newWatchAddCommand(),
		a.newWatchListCommand(),
		a.newWatchShowCommand(),
		a.newWatchCheckCommand(),
		a.newWatchEnableCommand(),
		a.newWatchDisableCommand(),
		a.newWatchChannelsCommand(),
		a.newWatchEventsCommand(),
		a.newWatchRemoveCommand(),
	)
	return cmd
}

func (a *App) newWatchAddCommand() *cobra.Command {
	var (
		tags            []string
		patterns        []string
		channels        []string
		noNotifyFailure bool
		disabled        bool
	)
	cmd := &cobra.Command{
		Use:   "add <image>[:tag]",
		Short: "Add a watch for an image tag or tag pattern",
		Long: `Add a watch. A tag watch reports digest drift for one tag; a pattern watch
reports tags that appear matching a glob. --tag is repeatable and creates one
watch per tag.

If the repository needs credentials the server answers 428 and the CLI prompts
for them, stores them and retries the request.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := apitypes.CreateWatchRequest{
				Image:    args[0],
				Tags:     tags,
				Patterns: patterns,
				Channels: channels,
				Disabled: disabled,
			}
			if noNotifyFailure {
				notify := false
				req.NotifyOnFailure = &notify
			}

			ctx, cancel := a.Context()
			defer cancel()

			resp, err := a.Client.CreateWatch(ctx, req)
			if err != nil {
				if !isCredentialsRequired(err) {
					return err
				}
				// 428: collect credentials for the registry the server named,
				// then replay the request once.
				host := credentialsRegistry(err)
				if host == "" {
					return err
				}
				if _, perr := a.promptAndStoreCredentials(host); perr != nil {
					return perr
				}
				resp, err = a.Client.CreateWatch(ctx, req)
				if err != nil {
					return err
				}
			}

			return a.Out.Print(resp, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(resp)
				}
				if len(resp.Created) > 0 {
					if err := o.Println("Created %d watch(es) for %s", len(resp.Created), resp.Image); err != nil {
						return err
					}
				}
				if len(resp.Existing) > 0 {
					if err := o.Println("Already watched: %s", strings.Join(resp.Existing, ", ")); err != nil {
						return err
					}
				}
				return writeWatchTable(o, resp.Watches)
			})
		},
	}
	flags := cmd.Flags()
	flags.StringSliceVar(&tags, "tag", nil, "tag to watch; repeatable, one watch per tag")
	flags.StringSliceVar(&patterns, "pattern", nil, "glob pattern for new tags; repeatable")
	flags.StringSliceVar(&channels, "channel", nil, "channel name to notify; repeatable")
	flags.BoolVar(&noNotifyFailure, "no-notify-on-failure", false, "do not notify when checks fail")
	flags.BoolVar(&disabled, "disabled", false, "create the watch disabled")
	return cmd
}

func (a *App) newWatchListCommand() *cobra.Command {
	var (
		enabled  bool
		disabled bool
		registry string
		search   string
		limit    int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List watches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if enabled && disabled {
				return fmt.Errorf("--enabled and --disabled are mutually exclusive")
			}

			query := watchQueryFromFlags(enabled, disabled, registry, search, limit)

			ctx, cancel := a.Context()
			defer cancel()

			list, err := a.Client.ListWatches(ctx, query)
			if err != nil {
				return err
			}
			return a.Out.Print(list, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(list)
				}
				return writeWatchTable(o, list.Items)
			})
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&enabled, "enabled", false, "only enabled watches")
	flags.BoolVar(&disabled, "disabled", false, "only disabled watches")
	flags.StringVar(&registry, "registry", "", "only watches on this registry host")
	flags.StringVar(&search, "search", "", "substring match on image, repository or ref")
	flags.IntVar(&limit, "limit", 0, "maximum rows to return")
	return cmd
}

func (a *App) newWatchShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one watch in detail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			watch, err := a.Client.GetWatch(ctx, args[0])
			if err != nil {
				return err
			}
			return a.Out.Print(watch, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(watch)
				}
				return o.KeyValues([][2]string{
					{"ID", watch.ID},
					{"Image", watch.Image},
					{"Registry", watch.Registry},
					{"Repository", watch.Repository},
					{"Kind", string(watch.Kind)},
					{"Ref", watch.Ref},
					{"Enabled", formatBool(watch.Enabled)},
					{"Notify on failure", formatBool(watch.NotifyOnFailure)},
					{"Channels", joinOr(watch.Channels, "(defaults)")},
					{"Last digest", shortDigest(watch.LastDigest)},
					{"Last checked", formatTime(watch.LastCheckedAt)},
					{"Last OK", formatTime(watch.LastOKAt)},
					{"Last error", orDash(watch.LastError)},
					{"Consecutive failures", fmt.Sprintf("%d", watch.ConsecutiveFailures)},
					{"Next attempt", formatTime(watch.NextAttemptAt)},
					{"Created", formatTime(&watch.CreatedAt)},
					{"Updated", formatTime(&watch.UpdatedAt)},
				})
			})
		},
	}
}

func (a *App) newWatchCheckCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "check <id>",
		Short: "Force an immediate check and show the result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			result, err := a.Client.CheckWatch(ctx, args[0])
			if err != nil {
				return err
			}
			return a.Out.Print(result, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(result)
				}
				if result.Error != "" {
					return o.KeyValues([][2]string{
						{"Result", "failed"},
						{"Error", result.Error},
					})
				}
				switch {
				case result.Baseline:
					return o.KeyValues([][2]string{
						{"Result", "baseline recorded (no change reported)"},
						{"Matched", fmt.Sprintf("%d", result.Matched)},
					})
				case result.Changed:
					return o.KeyValues([][2]string{
						{"Result", "digest changed"},
						{"Tag", result.Tag},
						{"Old digest", shortDigest(result.OldDigest)},
						{"New digest", shortDigest(result.NewDigest)},
						{"Notified", fmt.Sprintf("%d channel(s)", result.Notified)},
					})
				case len(result.NewTags) > 0:
					if err := o.KeyValues([][2]string{
						{"Result", fmt.Sprintf("%d new tag(s)", len(result.NewTags))},
						{"Matched", fmt.Sprintf("%d", result.Matched)},
						{"Notified", fmt.Sprintf("%d channel(s)", result.Notified)},
					}); err != nil {
						return err
					}
					rows := make([][]string, 0, len(result.NewTags))
					for _, t := range result.NewTags {
						rows = append(rows, []string{t.Tag, shortDigest(t.Digest)})
					}
					return o.Table([]string{"TAG", "DIGEST"}, rows)
				default:
					return o.KeyValues([][2]string{
						{"Result", "no change"},
						{"Matched", fmt.Sprintf("%d", result.Matched)},
					})
				}
			})
		},
	}
}

func (a *App) newWatchEnableCommand() *cobra.Command {
	return a.toggleWatchCommand("enable", true)
}

func (a *App) newWatchDisableCommand() *cobra.Command {
	return a.toggleWatchCommand("disable", false)
}

func (a *App) toggleWatchCommand(use string, enabled bool) *cobra.Command {
	action := "Disable"
	state := "disabled"
	if enabled {
		action = "Enable"
		state = "enabled"
	}
	return &cobra.Command{
		Use:   use + " <id>",
		Short: action + " a watch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			watch, err := a.Client.UpdateWatch(ctx, args[0], apitypes.UpdateWatchRequest{Enabled: &enabled})
			if err != nil {
				return err
			}
			return a.Out.Print(watch, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(watch)
				}
				return o.Println("%s is now %s", watch.ID, state)
			})
		},
	}
}

func (a *App) newWatchChannelsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "channels <id> <channel>...",
		Short: "Set the channels a watch notifies",
		Long: `Set the explicit channel subscriptions for a watch. Passing no channel
names clears the subscriptions, which makes the watch fall back to the default
channels.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			channels := args[1:]
			if channels == nil {
				channels = []string{}
			}
			ctx, cancel := a.Context()
			defer cancel()

			watch, err := a.Client.UpdateWatch(ctx, args[0], apitypes.UpdateWatchRequest{Channels: &channels})
			if err != nil {
				return err
			}
			return a.Out.Print(watch, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(watch)
				}
				if len(watch.Channels) == 0 {
					return o.Println("%s notifies the default channels", watch.ID)
				}
				return o.Println("%s notifies: %s", watch.ID, strings.Join(watch.Channels, ", "))
			})
		},
	}
}

func (a *App) newWatchEventsCommand() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "events <id>",
		Short: "Show the event history of a watch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			list, err := a.Client.WatchEvents(ctx, args[0], limit)
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
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum events to show")
	return cmd
}

func (a *App) newWatchRemoveCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a watch",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if !a.canPrompt() {
					return fmt.Errorf("%w: pass --yes to confirm deletion", ErrNonInteractive)
				}
				answer, err := a.promptLine(fmt.Sprintf("Delete watch %s? [y/N]", args[0]))
				if err != nil {
					return err
				}
				if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
					return a.Out.Println("Aborted.")
				}
			}
			ctx, cancel := a.Context()
			defer cancel()

			if err := a.Client.DeleteWatch(ctx, args[0]); err != nil {
				return err
			}
			return a.Out.Println("Deleted %s", args[0])
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// ---------- rendering ----------

func writeWatchTable(o *Output, watches []apitypes.Watch) error {
	rows := make([][]string, 0, len(watches))
	for _, w := range watches {
		state := "on"
		if !w.Enabled {
			state = "off"
		}
		if w.ConsecutiveFailures > 0 {
			state = "failing"
		}
		rows = append(rows, []string{
			w.ID,
			w.Image,
			string(w.Kind),
			w.Ref,
			state,
			shortDigest(w.LastDigest),
			formatTime(w.LastCheckedAt),
			joinOr(w.Channels, "(default)"),
		})
	}
	return o.Table([]string{"ID", "IMAGE", "KIND", "REF", "STATE", "DIGEST", "LAST CHECK", "CHANNELS"}, rows)
}

func writeEventTable(o *Output, events []apitypes.Event) error {
	rows := make([][]string, 0, len(events))
	for _, e := range events {
		rows = append(rows, []string{
			e.ID,
			formatTime(&e.CreatedAt),
			string(e.Type),
			e.Image,
			e.Tag,
			shortDigest(firstNonEmpty(e.NewDigest, e.OldDigest)),
			fmt.Sprintf("%d", len(e.Notifications)),
		})
	}
	return o.Table([]string{"ID", "WHEN", "TYPE", "IMAGE", "REF", "DIGEST", "SENT"}, rows)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
