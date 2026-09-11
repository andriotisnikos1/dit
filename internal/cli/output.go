package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Format is an output format.
type Format string

const (
	// FormatTable is the default human-readable output.
	FormatTable Format = "table"
	// FormatJSON emits raw API responses for scripting.
	FormatJSON Format = "json"
	// FormatYAML emits the same structures as YAML.
	FormatYAML Format = "yaml"
)

// ParseFormat validates a format name.
func ParseFormat(raw string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(FormatTable):
		return FormatTable, nil
	case string(FormatJSON):
		return FormatJSON, nil
	case string(FormatYAML):
		return FormatYAML, nil
	default:
		return "", fmt.Errorf("unknown output format %q: use table, json or yaml", raw)
	}
}

// Output renders results in the selected format.
type Output struct {
	Format Format
	Color  bool
	Writer io.Writer
}

// NewOutput builds an Output writing to w.
func NewOutput(w io.Writer) *Output {
	if w == nil {
		w = os.Stdout
	}
	return &Output{Format: FormatTable, Writer: w}
}

// Print renders a value: JSON and YAML get the raw structure, table gets
// whatever the caller passed as the table representation.
func (o *Output) Print(value any, table func(*Output) error) error {
	switch o.Format {
	case FormatJSON:
		return o.PrintJSON(value)
	case FormatYAML:
		return o.PrintYAML(value)
	default:
		if table == nil {
			return o.PrintJSON(value)
		}
		return table(o)
	}
}

// PrintJSON writes an indented JSON document.
func (o *Output) PrintJSON(value any) error {
	enc := json.NewEncoder(o.Writer)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// PrintYAML writes a YAML document.
func (o *Output) PrintYAML(value any) error {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode yaml: %w", err)
	}
	if _, err := o.Writer.Write(raw); err != nil {
		return fmt.Errorf("write yaml: %w", err)
	}
	return nil
}

// Table writes an aligned table. A nil or empty row set prints "none" so the
// operator never sees a bare header.
func (o *Output) Table(headers []string, rows [][]string) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(o.Writer, "none")
		return err
	}
	w := tabwriter.NewWriter(o.Writer, 0, 0, 2, ' ', 0)
	if len(headers) > 0 {
		if _, err := fmt.Fprintln(w, strings.Join(headers, "\t")); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(w, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return w.Flush()
}

// KeyValues writes a two-column label/value block.
func (o *Output) KeyValues(pairs [][2]string) error {
	w := tabwriter.NewWriter(o.Writer, 0, 0, 2, ' ', 0)
	for _, kv := range pairs {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", kv[0]+":", kv[1]); err != nil {
			return err
		}
	}
	return w.Flush()
}

// Println writes a plain line.
func (o *Output) Println(format string, args ...any) error {
	_, err := fmt.Fprintf(o.Writer, format+"\n", args...)
	return err
}

// Errorf writes to stderr; kept here so commands have one output surface.
func (o *Output) Errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// isTerminalWriter reports whether w is an interactive terminal.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// stdinIsTerminal reports whether stdin is interactive.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// ---------- shared formatting helpers ----------

// formatTime renders a timestamp for tables: relative for recent values,
// absolute otherwise. Empty values render as "-".
func formatTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	now := time.Now()
	delta := now.Sub(*t)
	switch {
	case delta < 0:
		// Scheduled in the future.
		if -delta < time.Minute {
			return "in <1m"
		}
		return "in " + roundDuration(-delta)
	case delta < time.Minute:
		return "just now"
	case delta < 24*time.Hour:
		return roundDuration(delta) + " ago"
	default:
		return t.UTC().Format("2006-01-02 15:04")
	}
}

func roundDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// formatBool renders a boolean for tables.
func formatBool(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// truncate shortens a string to n runes with an ellipsis.
func truncate(s string, n int) string {
	if n <= 1 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// shortDigest trims a digest to its algorithm and first 12 hex characters.
func shortDigest(d string) string {
	if d == "" {
		return "-"
	}
	if strings.HasPrefix(d, "sha256:") {
		hex := strings.TrimPrefix(d, "sha256:")
		if len(hex) > 12 {
			return "sha256:" + hex[:12]
		}
		return d
	}
	return truncate(d, 20)
}

// joinOr renders a list, or a placeholder when empty.
func joinOr(items []string, placeholder string) string {
	if len(items) == 0 {
		return placeholder
	}
	return strings.Join(items, ",")
}
