package cli

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// Version is stamped at build time with -ldflags.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// BuildInfo describes the running binary.
type BuildInfo struct {
	Version string `json:"version" yaml:"version"`
	Commit  string `json:"commit"  yaml:"commit"`
	Date    string `json:"date"    yaml:"date"`
	Go      string `json:"go"      yaml:"go"`
	OS      string `json:"os"      yaml:"os"`
	Arch    string `json:"arch"    yaml:"arch"`
}

// CurrentBuild returns the build metadata of this binary.
func CurrentBuild() BuildInfo {
	return BuildInfo{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}

func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Print the CLI version",
		Annotations: map[string]string{AnnotationNoClient: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := CurrentBuild()
			return a.Out.Print(info, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(info)
				}
				return o.Println("dit %s (commit %s, built %s, %s %s/%s)",
					info.Version, info.Commit, info.Date, info.Go, info.OS, info.Arch)
			})
		},
	}
}

func (a *App) newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show server status and watch counts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.Context()
			defer cancel()

			status, err := a.Client.Status(ctx)
			if err != nil {
				return err
			}
			return a.Out.Print(status, func(o *Output) error {
				if o.Format != FormatTable {
					return o.PrintJSON(status)
				}
				return o.KeyValues([][2]string{
					{"Server", a.Config.Server},
					{"Version", status.Version},
					{"Uptime", formatUptime(status.UptimeSeconds)},
					{"Watches", fmt.Sprintf("%d (%d enabled, %d disabled, %d failing)",
						status.Watches.Total, status.Watches.Enabled,
						status.Watches.Disabled, status.Watches.Failing)},
					{"Channels", fmt.Sprintf("%d", status.Channels)},
					{"Registries", fmt.Sprintf("%d", status.Registries)},
					{"Events", fmt.Sprintf("%d", status.Events)},
					{"Interval", status.CheckInterval},
					{"Concurrency", fmt.Sprintf("%d", status.CheckConcurrency)},
					{"Ticks", fmt.Sprintf("%d", status.Ticks)},
					{"Last tick", formatTime(status.LastTick)},
					{"Next tick", formatTime(status.NextTick)},
				})
			})
		},
	}
}

func formatUptime(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	parts := []string{}
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	return strings.Join(parts, " ")
}
