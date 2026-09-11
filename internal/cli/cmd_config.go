package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/andriotisnikos1/dit/internal/apiclient"
	"github.com/andriotisnikos1/dit/internal/config"
)

func (a *App) newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show and edit the CLI configuration",
		Long: `The CLI keeps exactly one piece of local state: a config file holding the
server URL and the shared secret, written 0600. Everything else lives on the
server.`,
		Annotations: map[string]string{AnnotationNoClient: "true"},
	}

	cmd.AddCommand(
		a.newConfigShowCommand(),
		a.newConfigPathCommand(),
		a.newConfigSetServerCommand(),
		a.newConfigSetTokenCommand(),
	)
	return cmd
}

func (a *App) newConfigShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "show",
		Short:       "Show the effective configuration with the token masked",
		Annotations: map[string]string{AnnotationNoClient: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.effectiveConfig()
			if err != nil {
				return err
			}
			redacted := cfg.RedactedMap()
			return a.Out.Print(redacted, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(redacted)
				}
				return o.KeyValues([][2]string{
					{"Path", redacted["path"]},
					{"Server", redacted["server"]},
					{"Token", redacted["token"]},
					{"Output", redacted["output"]},
				})
			})
		},
	}
}

func (a *App) newConfigPathCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "path",
		Short:       "Print the config file path",
		Annotations: map[string]string{AnnotationNoClient: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.Out.Println("%s", config.DefaultCLIPath())
		},
	}
}

func (a *App) newConfigSetServerCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "set-server <url>",
		Short:       "Set the dit-server base URL",
		Annotations: map[string]string{AnnotationNoClient: "true"},
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			normalised, err := apiclient.NormaliseURL(args[0])
			if err != nil {
				return err
			}
			return a.updateConfig(func(cfg *config.CLI) error {
				cfg.Server = normalised
				return nil
			})
		},
	}
}

func (a *App) newConfigSetTokenCommand() *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:         "set-token",
		Short:       "Set the shared API token",
		Annotations: map[string]string{AnnotationNoClient: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var token string
			if passwordStdin {
				raw, err := readPasswordStdin()
				if err != nil {
					return err
				}
				token = raw
			} else {
				prompted, err := a.promptSecret("API token")
				if err != nil {
					return fmt.Errorf("%w: pass --token, or use --password-stdin to pipe it in", err)
				}
				token = prompted
			}
			if strings.TrimSpace(token) == "" {
				return fmt.Errorf("token must not be empty")
			}
			return a.updateConfig(func(cfg *config.CLI) error {
				cfg.Token = strings.TrimSpace(token)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the token from stdin")
	return cmd
}

// updateConfig loads, mutates and saves the CLI config file.
func (a *App) updateConfig(mutate func(*config.CLI) error) error {
	cfg, err := config.LoadCLI(a.ConfigFlag)
	if err != nil {
		return err
	}
	// Start from what the file holds, not from flags: writing flags back would
	// silently persist a one-off override.
	fileCfg, err := config.LoadCLIFile(a.ConfigFlag)
	if err != nil {
		return err
	}
	_ = cfg
	if err := mutate(fileCfg); err != nil {
		return err
	}
	if err := fileCfg.Save(); err != nil {
		return err
	}
	return a.Out.Println("Updated %s", fileCfg.Path())
}

// effectiveConfig is the config the next command would use.
func (a *App) effectiveConfig() (*config.CLI, error) {
	cfg, err := config.LoadCLI(a.ConfigFlag)
	if err != nil {
		return nil, err
	}
	if a.ServerFlag != "" {
		cfg.Server = strings.TrimSpace(a.ServerFlag)
	}
	if a.TokenFlag != "" {
		cfg.Token = strings.TrimSpace(a.TokenFlag)
	}
	if a.OutputFlag != "" {
		cfg.Output = strings.TrimSpace(a.OutputFlag)
	}
	return cfg, nil
}
