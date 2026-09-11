// Package cli implements the dit command tree, interactive prompts and output
// rendering. The CLI is a stateless remote client: everything it shows comes
// from the server API.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/andriotisnikos1/dit/internal/apiclient"
	"github.com/andriotisnikos1/dit/internal/config"
)

// AnnotationNoClient marks a command that must not build an API client, such as
// `dit config path` or `dit version`.
const AnnotationNoClient = "dit.no_client"

// App carries the resolved CLI state between commands.
type App struct {
	Config *config.CLI
	Client apiclient.API
	Out    *Output

	// Flags.
	ServerFlag     string
	TokenFlag      string
	ConfigFlag     string
	OutputFlag     string
	NoColor        bool
	Verbose        bool
	NonInteractive bool

	// Factory builds the API client; overridable in tests.
	NewClient func(baseURL, token string) (apiclient.API, error)

	// Stdout and Stderr are the streams to write to.
	Stdout io.Writer
	Stderr io.Writer
}

// NewApp builds an App with production defaults.
func NewApp() *App {
	return &App{
		Out:       NewOutput(os.Stdout),
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		NewClient: func(baseURL, token string) (apiclient.API, error) { return apiclient.New(baseURL, token) },
	}
}

// NewRootCommand builds the whole command tree.
func NewRootCommand(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "dit",
		Short: "Watch Docker/OCI image refs for digest drift and new tags",
		Long: `dit watches Docker/OCI image references across public and private
registries, detects digest drift and newly published tags, and notifies over
email and/or ntfy.

The CLI is a stateless remote client: it holds only the server URL and the
shared secret, and every watch, channel and event lives on the server.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return app.prepare(cmd)
		},
	}

	flags := root.PersistentFlags()
	flags.StringVar(&app.ServerFlag, "server", "", "dit-server base URL (env DIT_SERVER_URL)")
	flags.StringVar(&app.TokenFlag, "token", "", "API token (env DIT_API_TOKEN)")
	flags.StringVar(&app.ConfigFlag, "config", "", "config file path (env DIT_CONFIG)")
	flags.StringVarP(&app.OutputFlag, "output", "o", "", "output format: table, json or yaml")
	flags.BoolVar(&app.NoColor, "no-color", false, "disable coloured output")
	flags.BoolVar(&app.Verbose, "verbose", false, "log requests to stderr")
	flags.BoolVar(&app.NonInteractive, "non-interactive", false, "never prompt; fail instead")

	root.AddCommand(
		app.newVersionCommand(),
		app.newConfigCommand(),
		app.newStatusCommand(),
		app.newWatchCommand(),
		app.newChannelCommand(),
		app.newCredsCommand(),
		app.newEventsCommand(),
		app.newNotificationsCommand(),
	)
	return root
}

// prepare resolves configuration and builds the API client.
func (a *App) prepare(cmd *cobra.Command) error {
	cfg, err := config.LoadCLI(a.ConfigFlag)
	if err != nil {
		return err
	}
	// Flags win over environment, which wins over the file.
	if a.ServerFlag != "" {
		cfg.Server = strings.TrimSpace(a.ServerFlag)
	}
	if a.TokenFlag != "" {
		cfg.Token = strings.TrimSpace(a.TokenFlag)
	}
	if a.OutputFlag != "" {
		cfg.Output = strings.TrimSpace(a.OutputFlag)
	}
	a.Config = cfg

	format, err := ParseFormat(cfg.Output)
	if err != nil {
		return err
	}
	a.Out = NewOutput(a.Stdout)
	a.Out.Format = format
	a.Out.Color = !a.NoColor && isTerminalWriter(a.Stdout)

	if cmd.Annotations[AnnotationNoClient] == "true" {
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	client, err := a.NewClient(cfg.Server, cfg.Token)
	if err != nil {
		return err
	}
	a.Client = client
	return nil
}

// Context returns a context cancelled on SIGINT/SIGTERM.
func (a *App) Context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Execute runs the CLI and returns a process exit code.
func Execute() int {
	app := NewApp()
	root := NewRootCommand(app)

	if err := root.Execute(); err != nil {
		fmt.Fprintf(app.Stderr, "dit: %v\n", err)
		var apiErr apiError
		if errors.As(err, &apiErr) && apiErr.UsageError() {
			return 2
		}
		return 1
	}
	return 0
}

// apiError is the minimal surface Execute needs to pick an exit code.
type apiError interface {
	error
	UsageError() bool
}
