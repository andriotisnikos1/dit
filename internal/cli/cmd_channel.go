package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
)

func (a *App) newChannelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "Manage notification channels",
		Long: `Channels are reusable notification targets. A watch with no explicit
subscriptions notifies the channels flagged as default.`,
	}
	cmd.AddCommand(
		a.newChannelAddCommand(),
		a.newChannelListCommand(),
		a.newChannelShowCommand(),
		a.newChannelTestCommand(),
		a.newChannelDefaultCommand(),
		a.newChannelEnableCommand(),
		a.newChannelDisableCommand(),
		a.newChannelRemoveCommand(),
	)
	return cmd
}

func (a *App) newChannelAddCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <type>",
		Short: "Add an email, email-http or ntfy channel",
		Args:  cobra.ExactArgs(1),
	}
	cmd.AddCommand(
		a.newChannelAddEmailCommand(),
		a.newChannelAddEmailHTTPCommand(),
		a.newChannelAddNtfyCommand(),
	)
	return cmd
}

func (a *App) newChannelAddEmailCommand() *cobra.Command {
	var (
		name        string
		host        string
		port        int
		startTLS    bool
		implicitTLS bool
		from        string
		to          []string
		username    string
		passwordIn  bool
	)
	cmd := &cobra.Command{
		Use:   "email",
		Short: "Add an SMTP email channel",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if host == "" {
				return fmt.Errorf("--smtp-host is required")
			}
			if from == "" {
				return fmt.Errorf("--from is required")
			}
			if len(to) == 0 {
				return fmt.Errorf("--to is required (repeatable)")
			}
			if startTLS && implicitTLS {
				return fmt.Errorf("--starttls and --implicit-tls are mutually exclusive")
			}

			cfg := map[string]string{
				apitypes.ConfigSMTPHost: host,
				apitypes.ConfigFrom:     from,
				apitypes.ConfigTo:       strings.Join(to, ","),
			}
			if port > 0 {
				cfg[apitypes.ConfigSMTPPort] = strconv.Itoa(port)
			}
			if startTLS {
				cfg[apitypes.ConfigSTARTTLS] = "true"
			}
			if implicitTLS {
				cfg[apitypes.ConfigImplicitTLS] = "true"
			}
			if username != "" {
				cfg[apitypes.ConfigUsername] = username
			}

			// Only prompt for a password when one is actually needed.
			if username != "" || passwordIn {
				password, err := a.channelPassword(passwordIn, "SMTP password")
				if err != nil {
					return err
				}
				if password != "" {
					cfg[apitypes.ConfigPassword] = password
				}
			}

			ctx, cancel := a.Context()
			defer cancel()

			channel, err := a.Client.CreateChannel(ctx, apitypes.CreateChannelRequest{
				Name:   name,
				Type:   apitypes.ChannelEmail,
				Config: cfg,
			})
			if err != nil {
				return err
			}
			return a.printChannelCreated(channel)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&name, "name", "", "channel name (required)")
	flags.StringVar(&host, "smtp-host", "", "SMTP host (required)")
	flags.IntVar(&port, "smtp-port", 587, "SMTP port")
	flags.BoolVar(&startTLS, "starttls", false, "upgrade the connection with STARTTLS")
	flags.BoolVar(&implicitTLS, "implicit-tls", false, "use TLS from the first byte")
	flags.StringVar(&from, "from", "", "From address (required)")
	flags.StringSliceVar(&to, "to", nil, "recipient address (required, repeatable)")
	flags.StringVar(&username, "username", "", "SMTP username")
	flags.BoolVar(&passwordIn, "password-stdin", false, "read the SMTP password from stdin")
	return cmd
}

// newChannelAddEmailHTTPCommand adds a channel that sends through a
// transactional email provider's HTTPS API.
func (a *App) newChannelAddEmailHTTPCommand() *cobra.Command {
	var (
		name      string
		provider  string
		apiKeyIn  bool
		from      string
		fromName  string
		to        []string
		accountID string
		baseURL   string
	)
	cmd := &cobra.Command{
		Use:   "email-http",
		Short: "Add a transactional email channel that sends over an HTTPS API",
		Long: "Add an email channel that delivers through a provider's HTTP API\n" +
			"instead of SMTP.\n\n" +
			"Use this where outbound SMTP is blocked or unavailable — Railway\n" +
			"disables SMTP below its Pro plan, for instance, while ordinary\n" +
			"outbound HTTPS keeps working.\n\n" +
			"Supported providers: " + strings.Join(notify.ProviderNames(), ", ") + ".\n" +
			"cloudflare additionally needs --account-id, since its endpoint is\n" +
			"per-account.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if provider == "" {
				return fmt.Errorf("--provider is required (one of %s)",
					strings.Join(notify.ProviderNames(), ", "))
			}
			if from == "" {
				return fmt.Errorf("--from is required")
			}
			if len(to) == 0 {
				return fmt.Errorf("--to is required (repeatable)")
			}

			cfg := map[string]string{
				apitypes.ConfigProvider: provider,
				apitypes.ConfigFrom:     from,
				apitypes.ConfigTo:       strings.Join(to, ","),
			}
			if fromName != "" {
				cfg[apitypes.ConfigFromName] = fromName
			}
			if accountID != "" {
				cfg[apitypes.ConfigAccountID] = accountID
			}
			if baseURL != "" {
				cfg[apitypes.ConfigURL] = baseURL
			}

			key, err := a.channelAPIKey(apiKeyIn)
			if err != nil {
				return err
			}
			if key != "" {
				cfg[apitypes.ConfigAPIKey] = key
			}

			ctx, cancel := a.Context()
			defer cancel()

			channel, err := a.Client.CreateChannel(ctx, apitypes.CreateChannelRequest{
				Name:   name,
				Type:   apitypes.ChannelEmailHTTP,
				Config: cfg,
			})
			if err != nil {
				return err
			}
			return a.printChannelCreated(channel)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&name, "name", "", "channel name (required)")
	flags.StringVar(&provider, "provider", "",
		"email provider: "+strings.Join(notify.ProviderNames(), ", ")+" (required)")
	flags.BoolVar(&apiKeyIn, "api-key-stdin", false, "read the provider API key from stdin")
	flags.StringVar(&from, "from", "", "From address (required)")
	flags.StringVar(&fromName, "from-name", "", "display name for the From address")
	flags.StringSliceVar(&to, "to", nil, "recipient address (required, repeatable)")
	flags.StringVar(&accountID, "account-id", "", "provider account id (required by cloudflare)")
	flags.StringVar(&baseURL, "url", "", "override the provider API base URL")
	return cmd
}

// channelAPIKey collects a provider API key the same way other channel secrets
// are collected: stdin when asked, a hidden prompt when interactive.
func (a *App) channelAPIKey(fromStdin bool) (string, error) {
	if fromStdin {
		return readPasswordStdin()
	}
	if !a.canPrompt() {
		return "", nil
	}
	value, err := a.promptSecret("Provider API key (leave empty to skip)")
	if err != nil {
		return "", err
	}
	return value, nil
}

func (a *App) newChannelAddNtfyCommand() *cobra.Command {
	var (
		name      string
		url       string
		topic     string
		token     string
		tokenIn   bool
		priority  string
		tags      []string
		fromStdin bool
	)
	cmd := &cobra.Command{
		Use:   "ntfy",
		Short: "Add an ntfy channel",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if topic == "" {
				return fmt.Errorf("--topic is required")
			}

			cfg := map[string]string{
				apitypes.ConfigTopic: topic,
			}
			if url != "" {
				cfg[apitypes.ConfigURL] = url
			}
			if priority != "" {
				cfg[apitypes.ConfigPriority] = priority
			}
			if len(tags) > 0 {
				cfg[apitypes.ConfigTags] = strings.Join(tags, ",")
			}
			if token != "" {
				cfg[apitypes.ConfigToken] = token
			}
			if tokenIn {
				secret, err := readPasswordStdin()
				if err != nil {
					return err
				}
				cfg[apitypes.ConfigToken] = secret
			}
			_ = fromStdin

			ctx, cancel := a.Context()
			defer cancel()

			channel, err := a.Client.CreateChannel(ctx, apitypes.CreateChannelRequest{
				Name:   name,
				Type:   apitypes.ChannelNtfy,
				Config: cfg,
			})
			if err != nil {
				return err
			}
			return a.printChannelCreated(channel)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&name, "name", "", "channel name (required)")
	flags.StringVar(&url, "url", "", "ntfy server URL (default https://ntfy.sh)")
	flags.StringVar(&topic, "topic", "", "ntfy topic (required)")
	flags.StringVar(&token, "token", "", "ntfy access token")
	flags.BoolVar(&tokenIn, "token-stdin", false, "read the ntfy token from stdin")
	flags.StringVar(&priority, "priority", "default", "default priority: min, low, default, high, max")
	flags.StringSliceVar(&tags, "tags", nil, "default ntfy tags; repeatable")
	return cmd
}

// channelPassword collects a secret for a channel: stdin when asked, a hidden
// prompt when interactive, empty otherwise (the secret is optional).
func (a *App) channelPassword(fromStdin bool, label string) (string, error) {
	if fromStdin {
		return readPasswordStdin()
	}
	if !a.canPrompt() {
		return "", nil
	}
	value, err := a.promptSecret(label + " (leave empty to skip)")
	if err != nil {
		return "", err
	}
	return value, nil
}

func (a *App) newChannelListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List channels",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			list, err := a.Client.ListChannels(ctx)
			if err != nil {
				return err
			}
			return a.Out.Print(list, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(list)
				}
				return writeChannelTable(o, list.Items)
			})
		},
	}
}

func (a *App) newChannelShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			channel, err := a.Client.GetChannel(ctx, args[0])
			if err != nil {
				return err
			}
			return a.Out.Print(channel, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(channel)
				}
				pairs := [][2]string{
					{"ID", channel.ID},
					{"Name", channel.Name},
					{"Type", string(channel.Type)},
					{"Enabled", formatBool(channel.Enabled)},
					{"Default", formatBool(channel.IsDefault)},
					{"Has secret", formatBool(channel.HasSecret)},
				}
				for _, key := range sortedKeys(channel.Config) {
					if channel.Config[key] == "" {
						continue
					}
					pairs = append(pairs, [2]string{"  " + key, channel.Config[key]})
				}
				return o.KeyValues(pairs)
			})
		},
	}
}

func (a *App) newChannelTestCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "test <id>",
		Short: "Send a test notification through a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			result, err := a.Client.TestChannel(ctx, args[0])
			if err != nil {
				return err
			}
			if !result.OK {
				return fmt.Errorf("test notification failed: %s", result.Error)
			}
			return a.Out.Println("Test notification sent via %s", result.ChannelID)
		},
	}
}

func (a *App) newChannelDefaultCommand() *cobra.Command {
	var off bool
	cmd := &cobra.Command{
		Use:   "default <id>",
		Short: "Mark or unmark a channel as a default target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			value := !off
			ctx, cancel := a.Context()
			defer cancel()

			channel, err := a.Client.UpdateChannel(ctx, args[0], apitypes.UpdateChannelRequest{IsDefault: &value})
			if err != nil {
				return err
			}
			if channel.IsDefault {
				return a.Out.Println("%s is a default channel", channel.ID)
			}
			return a.Out.Println("%s is no longer a default channel", channel.ID)
		},
	}
	cmd.Flags().BoolVar(&off, "off", false, "unmark as default")
	return cmd
}

func (a *App) newChannelEnableCommand() *cobra.Command {
	return a.toggleChannelCommand("enable", true)
}

func (a *App) newChannelDisableCommand() *cobra.Command {
	return a.toggleChannelCommand("disable", false)
}

func (a *App) toggleChannelCommand(use string, enabled bool) *cobra.Command {
	action, state := "Disable", "disabled"
	if enabled {
		action, state = "Enable", "enabled"
	}
	return &cobra.Command{
		Use:   use + " <id>",
		Short: action + " a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			channel, err := a.Client.UpdateChannel(ctx, args[0], apitypes.UpdateChannelRequest{Enabled: &enabled})
			if err != nil {
				return err
			}
			return a.Out.Println("%s is now %s", channel.ID, state)
		},
	}
}

func (a *App) newChannelRemoveCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a channel",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if !a.canPrompt() {
					return fmt.Errorf("%w: pass --yes to confirm deletion", ErrNonInteractive)
				}
				answer, err := a.promptLine(fmt.Sprintf("Delete channel %s? [y/N]", args[0]))
				if err != nil {
					return err
				}
				if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
					return a.Out.Println("Aborted.")
				}
			}
			ctx, cancel := a.Context()
			defer cancel()

			if err := a.Client.DeleteChannel(ctx, args[0]); err != nil {
				return err
			}
			return a.Out.Println("Deleted %s", args[0])
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func (a *App) printChannelCreated(channel apitypes.Channel) error {
	return a.Out.Print(channel, func(o *Output) error {
		if o.Format != FormatTable {
			return o.PrintJSON(channel)
		}
		return o.Println("Created %s channel %s (%s)", channel.Type, channel.Name, channel.ID)
	})
}

func writeChannelTable(o *Output, channels []apitypes.Channel) error {
	rows := make([][]string, 0, len(channels))
	for _, c := range channels {
		state := "on"
		if !c.Enabled {
			state = "off"
		}
		rows = append(rows, []string{
			c.ID,
			c.Name,
			string(c.Type),
			state,
			formatBool(c.IsDefault),
			formatBool(c.HasSecret),
			channelSummary(c),
		})
	}
	return o.Table([]string{"ID", "NAME", "TYPE", "STATE", "DEFAULT", "SECRET", "DETAIL"}, rows)
}

func channelSummary(c apitypes.Channel) string {
	switch c.Type {
	case apitypes.ChannelEmail:
		host := c.Config[apitypes.ConfigSMTPHost]
		port := c.Config[apitypes.ConfigSMTPPort]
		to := c.Config[apitypes.ConfigTo]
		return fmt.Sprintf("%s:%s -> %s", host, port, truncate(to, 40))
	case apitypes.ChannelEmailHTTP:
		return fmt.Sprintf("%s -> %s",
			c.Config[apitypes.ConfigProvider], truncate(c.Config[apitypes.ConfigTo], 40))
	case apitypes.ChannelNtfy:
		url := c.Config[apitypes.ConfigURL]
		if url == "" {
			url = "https://ntfy.sh"
		}
		return fmt.Sprintf("%s/%s", url, c.Config[apitypes.ConfigTopic])
	default:
		return ""
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Small maps: insertion sort keeps the dependency list short.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
