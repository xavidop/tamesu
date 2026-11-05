package report

import (
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"
)

// Style colours console output with ANSI codes, or leaves it alone. A zero
// Style never colours, so every writer that is not a terminal stays plain.
type Style struct {
	enabled bool
}

// NewStyle returns a Style that colours when enabled is true.
func NewStyle(enabled bool) Style { return Style{enabled: enabled} }

// Enabled reports whether the style colours.
func (s Style) Enabled() bool { return s.enabled }

// ColorModes are the values of --color.
var ColorModes = []string{"auto", "always", "never"}

// ColorEnabled decides whether output to w gets colours: never and always
// say so; auto colours a terminal unless NO_COLOR is set, and anything when
// FORCE_COLOR is set (no-color.org and force-color.org conventions). A dumb
// terminal and, on Windows, a console that does not announce ANSI support
// (no WT_SESSION, TERM or ANSICON) stay plain.
func ColorEnabled(mode string, w io.Writer, env func(string) (string, bool)) bool {
	if env == nil {
		env = func(string) (string, bool) { return "", false }
	}
	switch mode {
	case "never":
		return false
	case "always":
		return true
	}
	if v, ok := env("NO_COLOR"); ok && v != "" {
		return false
	}
	if v, ok := env("FORCE_COLOR"); ok && v != "" && v != "0" {
		return true
	}
	if term, _ := env("TERM"); term == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"WT_SESSION", "TERM", "ANSICON"} {
			if v, _ := env(k); v != "" {
				return true
			}
		}
		return false
	}
	return true
}

const (
	codeReset  = "\x1b[0m"
	codeBold   = "\x1b[1m"
	codeDim    = "\x1b[2m"
	codeRed    = "\x1b[31m"
	codeGreen  = "\x1b[32m"
	codeYellow = "\x1b[33m"
	codeCyan   = "\x1b[36m"
)

func (s Style) wrap(code, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	return code + text + codeReset
}

// Pass is green and bold: a passed test, a healthy check.
func (s Style) Pass(text string) string { return s.wrap(codeBold+codeGreen, text) }

// Fail is red and bold: a failed test, an error, a failed check.
func (s Style) Fail(text string) string { return s.wrap(codeBold+codeRed, text) }

// Warn is yellow: a warning, a quarantined test, a lint finding.
func (s Style) Warn(text string) string { return s.wrap(codeYellow, text) }

// Info is cyan: a heading or a path worth spotting.
func (s Style) Info(text string) string { return s.wrap(codeCyan, text) }

// Bold is bold.
func (s Style) Bold(text string) string { return s.wrap(codeBold, text) }

// Dim is faint: skipped tests, transcript excerpts, progress.
func (s Style) Dim(text string) string { return s.wrap(codeDim, text) }

// ansiCode matches one ANSI style sequence.
var ansiCode = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Table aligns columns the way text/tabwriter does, but measures cells
// without their colour codes, so coloured tables line up. Header cells are
// bold.
type Table struct {
	w    io.Writer
	st   Style
	rows [][]string
}

// NewTable starts a table on w with a bold header row.
func (s Style) NewTable(w io.Writer, header ...string) *Table {
	t := &Table{w: w, st: s}
	bold := make([]string, len(header))
	for i, h := range header {
		bold[i] = s.Bold(h)
	}
	t.rows = append(t.rows, bold)
	return t
}

// Row adds a row; cells may carry colour codes.
func (t *Table) Row(cells ...string) { t.rows = append(t.rows, cells) }

// Flush writes the table: columns two spaces apart, the last one unpadded.
func (t *Table) Flush() error {
	var widths []int
	for _, row := range t.rows {
		for i, c := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], textWidth(c))
		}
	}
	var b strings.Builder
	for _, row := range t.rows {
		for i, c := range row {
			b.WriteString(c)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-textWidth(c)+2))
			}
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(t.w, b.String())
	return err
}

// textWidth is the width of text without its colour codes.
func textWidth(s string) int { return utf8.RuneCountInString(Strip(s)) }

// Strip removes the ANSI codes a Style adds, for logs and tests.
func Strip(text string) string { return ansiCode.ReplaceAllString(text, "") }
