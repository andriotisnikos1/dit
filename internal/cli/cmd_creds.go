package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func (a *App) newCredsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "creds",
		Short: "Manage registry credentials",
		Long: `Registry credentials are stored on the server, sealed with NaCl secretbox.
They are write-only over the API: reads return metadata, never the secret.

This is the non-interactive equivalent of the prompt that ` + "`dit watch add`" + `
offers when a registry answers 401.`,
	}
	cmd.AddCommand(
		a.newCredsSetCommand(),
		a.newCredsListCommand(),
		a.newCredsRemoveCommand(),
	)
	return cmd
}

func (a *App) newCredsSetCommand() *cobra.Command {
	var (
		username   string
		passwordIn bool
		kind       string
	)
	cmd := &cobra.Command{
		Use:   "set <registry>",
		Short: "Store credentials for a registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := strings.TrimSpace(args[0])
			if host == "" {
				return fmt.Errorf("registry host is required")
			}
			if kind != apitypes.CredentialBasic && kind != apitypes.CredentialToken {
				return fmt.Errorf("--kind must be %q or %q", apitypes.CredentialBasic, apitypes.CredentialToken)
			}

			user, password, err := a.resolveCredentials(host, username, passwordIn)
			if err != nil {
				return err
			}

			ctx, cancel := a.Context()
			defer cancel()

			info, err := a.Client.PutCredentials(ctx, host, apitypes.CredentialsRequest{
				Username: user,
				Password: password,
				Kind:     kind,
			})
			if err != nil {
				return err
			}
			return a.Out.Print(info, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(info)
				}
				return o.Println("Stored credentials for %s (user %s, kind %s)",
					info.Host, orDash(info.Username), info.Kind)
			})
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&username, "username", "", "registry username")
	flags.BoolVar(&passwordIn, "password-stdin", false, "read the password or token from stdin")
	flags.StringVar(&kind, "kind", apitypes.CredentialBasic, "credential kind: basic or token")
	return cmd
}

func (a *App) newCredsListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registries and whether credentials are stored",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			list, err := a.Client.ListRegistries(ctx)
			if err != nil {
				return err
			}
			return a.Out.Print(list, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(list)
				}
				rows := make([][]string, 0, len(list.Items))
				for _, r := range list.Items {
					rows = append(rows, []string{
						r.Host,
						formatBool(r.HasCredentials),
						orDash(r.Username),
						orDash(r.Kind),
						fmt.Sprintf("%d", r.Watches),
						formatTime(r.LastUsedAt),
						formatTime(r.LastOKAt),
					})
				}
				return o.Table([]string{"REGISTRY", "CREDS", "USERNAME", "KIND", "WATCHES", "LAST USED", "LAST OK"}, rows)
			})
		},
	}
}

func (a *App) newCredsRemoveCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <registry>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete stored credentials for a registry",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if !a.canPrompt() {
					return fmt.Errorf("%w: pass --yes to confirm deletion", ErrNonInteractive)
				}
				answer, err := a.promptLine(fmt.Sprintf("Delete credentials for %s? [y/N]", args[0]))
				if err != nil {
					return err
				}
				if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
					return a.Out.Println("Aborted.")
				}
			}
			ctx, cancel := a.Context()
			defer cancel()

			if err := a.Client.DeleteCredentials(ctx, args[0]); err != nil {
				return err
			}
			return a.Out.Println("Deleted credentials for %s", args[0])
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
