package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// IsJSON checks whether the --json persistent flag is set.
func IsJSON(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// PrintJSON writes indented JSON to stdout.
func PrintJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Table wraps a tabwriter for aligned column output.
type Table struct {
	w *tabwriter.Writer
}

// NewTable creates a new Table writing to stdout.
func NewTable() *Table {
	return &Table{
		w: tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0),
	}
}

// Header writes a header row.
func (t *Table) Header(cols ...string) {
	for i, col := range cols {
		if i > 0 {
			_, _ = fmt.Fprint(t.w, "\t")
		}
		_, _ = fmt.Fprint(t.w, col)
	}
	_, _ = fmt.Fprintln(t.w)
}

// Row writes a data row.
func (t *Table) Row(cols ...string) {
	for i, col := range cols {
		if i > 0 {
			_, _ = fmt.Fprint(t.w, "\t")
		}
		_, _ = fmt.Fprint(t.w, col)
	}
	_, _ = fmt.Fprintln(t.w)
}

// Flush flushes the underlying tabwriter.
func (t *Table) Flush() {
	_ = t.w.Flush()
}

// FormatTime returns a human-readable relative time string.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// StatusIcon returns a unicode icon for a deployment status.
func StatusIcon(status string) string {
	switch status {
	case "running":
		return "●"
	case "pending", "in_progress":
		return "◐"
	case "failed":
		return "✗"
	case "stopped":
		return "○"
	default:
		return "?"
	}
}
