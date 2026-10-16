package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/flags"
)

func (a *App) newFlagCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flag",
		Short: "Inspect and set behaviour flags",
		Long: `Behaviour flags change how dit treats your watches. Each flag can be set
globally and overridden per watch; a watch's effective value is its own
override, then the global value, then the flag default.

Flags:
` + flagDescriptions() + `

Pass --watch to any subcommand to act on one watch instead of globally.`,
	}
	cmd.AddCommand(
		a.newFlagListCommand(),
		a.newFlagSetCommand(),
		a.newFlagUnsetCommand(),
	)
	return cmd
}

func flagDescriptions() string {
	var b strings.Builder
	for _, def := range flags.Definitions() {
		fmt.Fprintf(&b, "  %-24s (%s) %s\n", def.Name, def.Type, def.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *App) newFlagListCommand() *cobra.Command {
	var watch string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List behaviour flags and their values",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.Context()
			defer cancel()
			list, err := a.Client.ListFlags(ctx, watch)
			if err != nil {
				return err
			}
			return a.Out.Print(list, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(list)
				}
				return writeFlagTable(o, list, watch != "")
			})
		},
	}
	cmd.Flags().StringVar(&watch, "watch", "", "include per-watch override and effective values for this watch ID")
	return cmd
}

func writeFlagTable(o *Output, list apitypes.FlagList, withWatch bool) error {
	headers := []string{"NAME", "TYPE", "DEFAULT"}
	if withWatch {
		headers = append(headers, "LOCAL", "EFFECTIVE")
	} else {
		headers = append(headers, "GLOBAL")
	}
	rows := make([][]string, 0, len(list))
	for _, f := range list {
		row := []string{f.Name, string(f.Type), fmt.Sprintf("%v", f.Default)}
		if withWatch {
			row = append(row, flagValue(f.Local), flagValue(f.Effective))
		} else {
			row = append(row, flagValue(f.Global))
		}
		rows = append(rows, row)
	}
	return o.Table(headers, rows)
}

func flagValue(v *any) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%v", *v)
}

func (a *App) newFlagSetCommand() *cobra.Command {
	var watch string
	cmd := &cobra.Command{
		Use:   "set <name> <value>",
		Short: "Set a behaviour flag globally or on a watch",
		Args:  cobra.ArbitraryArgs, // validated below, after the trailing options are split off
		RunE: func(cmd *cobra.Command, args []string) error {
			positionals, reparse, err := splitFlagTail(cmd, args)
			if err != nil {
				return err
			}
			if len(positionals) != 2 {
				return fmt.Errorf("accepts 2 arg(s), received %d", len(positionals))
			}
			if reparse {
				// Options after the positionals were parsed after the
				// persistent pre-run hook, so re-resolve them.
				if err := a.prepare(cmd); err != nil {
					return err
				}
			}
			name, raw := positionals[0], positionals[1]
			def, ok := flags.Lookup(name)
			if !ok {
				return fmt.Errorf("unknown flag %q; run `dit flag list`", name)
			}
			value, err := flags.ParseValue(def, raw)
			if err != nil {
				return err
			}
			ctx, cancel := a.Context()
			defer cancel()
			flag, err := a.Client.SetFlag(ctx, watch, name, value)
			if err != nil {
				return err
			}
			scope := "globally"
			if watch != "" {
				scope = "on " + watch
			}
			return a.Out.Print(flag, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(flag)
				}
				return o.Println("Set %s = %v %s", name, value, scope)
			})
		},
	}
	cmd.Flags().StringVar(&watch, "watch", "", "set the flag on this watch ID instead of globally")
	// `flag set history -1` is the documented way to keep every event, but pflag
	// reads a leading dash as an option and fails with
	// "unknown shorthand flag: '1' in -1". Stopping option parsing at the first
	// positional keeps the value intact; options written after the positionals
	// are re-parsed by splitFlagTail, and a literal -- is honoured as the
	// explicit end of options, so `flag set history -- -1` keeps working too.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// splitFlagTail separates the positional arguments of `flag set` from any
// options that follow them.
//
// Option parsing stops at the first positional (so a negative value survives),
// which leaves trailing options in args; they are parsed here instead. Tokens
// that parse as a number are values, not options. reparse reports whether
// options were parsed after the initial pass, so the caller can re-resolve the
// configuration they set.
func splitFlagTail(cmd *cobra.Command, args []string) (positionals []string, reparse bool, err error) {
	positionals = make([]string, 0, len(args))
	for i, arg := range args {
		switch {
		case arg == "--":
			// An explicit separator: everything after it is a value.
			return append(positionals, args[i+1:]...), false, nil
		case looksLikeOption(arg):
			if err := cmd.Flags().Parse(args[i:]); err != nil {
				return nil, false, err
			}
			return positionals, true, nil
		default:
			positionals = append(positionals, arg)
		}
	}
	return positionals, false, nil
}

// looksLikeOption reports whether a token is an option rather than a value. A
// number with a leading dash ("-1") is a value.
func looksLikeOption(tok string) bool {
	if len(tok) < 2 || tok[0] != '-' {
		return false
	}
	if _, err := strconv.ParseFloat(tok, 64); err == nil {
		return false
	}
	return true
}

func (a *App) newFlagUnsetCommand() *cobra.Command {
	var watch string
	cmd := &cobra.Command{
		Use:   "unset <name>",
		Short: "Remove a flag override, global or per watch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, ok := flags.Lookup(args[0]); !ok {
				return fmt.Errorf("unknown flag %q; run `dit flag list`", args[0])
			}
			ctx, cancel := a.Context()
			defer cancel()
			if err := a.Client.UnsetFlag(ctx, watch, args[0]); err != nil {
				return err
			}
			scope := "global"
			if watch != "" {
				scope = "watch " + watch
			}
			return a.Out.Println("Removed %s override (%s)", args[0], scope)
		},
	}
	cmd.Flags().StringVar(&watch, "watch", "", "unset the flag on this watch ID instead of globally")
	return cmd
}
