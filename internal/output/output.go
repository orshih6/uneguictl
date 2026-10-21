// Package output renders command results as aligned text or JSON.
package output

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"
	"text/tabwriter"
	"unicode/utf8"
)

// JSON writes v as indented JSON.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Table writes rows under headers in aligned columns.
func Table(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

// Field is one line of a Fields listing.
type Field struct {
	Name, Value string
}

// Fields writes name/value pairs one per line, skipping empty values. A
// multi-line value is indented under its name.
func Fields(w io.Writer, fields []Field) error {
	width := 0
	for _, f := range fields {
		if n := utf8.RuneCountInString(f.Name); f.Value != "" && n > width {
			width = n
		}
	}
	for _, f := range fields {
		if f.Value == "" {
			continue
		}
		if strings.Contains(f.Value, "\n") {
			indented := "  " + strings.ReplaceAll(f.Value, "\n", "\n  ")
			if _, err := fmt.Fprintf(w, "%s:\n%s\n", f.Name, indented); err != nil {
				return err
			}
			continue
		}
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(f.Name))
		if _, err := fmt.Fprintf(w, "%s:%s  %s\n", f.Name, pad, f.Value); err != nil {
			return err
		}
	}
	return nil
}

// Truncate shortens s to at most max characters on one line, marking a cut
// with an ellipsis. Search results arrive with HTML entities in their
// titles ("don&#39;t"), so those are decoded on the way.
func Truncate(s string, max int) string {
	s = strings.Join(strings.Fields(html.UnescapeString(s)), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}
