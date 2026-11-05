package report

import (
	"io"
	"os"
	"runtime"
	"strings"
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

// Strip removes the ANSI codes a Style adds, for logs and tests.
func Strip(text string) string {
	for _, code := range []string{codeReset, codeBold, codeDim, codeRed, codeGreen, codeYellow, codeCyan} {
		text = strings.ReplaceAll(text, code, "")
	}
	return text
}
