package config

import (
	"fmt"
	"sort"
	"strings"
)

// Error is one problem found in a configuration or suite file. Warning errors
// do not fail validation on their own.
type Error struct {
	File    string
	Line    int
	Col     int
	Path    string
	Msg     string
	Warning bool
}

// Error formats as "<file>:<line>: [warning: ][<path>: ]<msg>", omitting the
// parts that are unknown.
func (e *Error) Error() string {
	var b strings.Builder
	if e.File != "" {
		b.WriteString(e.File)
		if e.Line > 0 {
			fmt.Fprintf(&b, ":%d", e.Line)
		}
		b.WriteString(": ")
	}
	if e.Warning {
		b.WriteString("warning: ")
	}
	if e.Path != "" {
		b.WriteString(e.Path)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

// Errors is the list of problems of one file, warnings included.
type Errors []*Error

// Error joins every entry on its own line.
func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

// HasErrors reports whether any entry is an error rather than a warning.
func (es Errors) HasErrors() bool {
	for _, e := range es {
		if !e.Warning {
			return true
		}
	}
	return false
}

// Only returns the errors, without the warnings.
func (es Errors) Only() Errors {
	var out Errors
	for _, e := range es {
		if !e.Warning {
			out = append(out, e)
		}
	}
	return out
}

// Warnings returns the warnings only.
func (es Errors) Warnings() Errors {
	var out Errors
	for _, e := range es {
		if e.Warning {
			out = append(out, e)
		}
	}
	return out
}

// Sorted returns the entries ordered by line, keeping the discovery order of
// entries on the same line.
func (es Errors) Sorted() Errors {
	out := make(Errors, len(es))
	copy(out, es)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ErrOrNil returns es as an error when it has entries, else nil, so a caller
// can write `return errs.ErrOrNil()`.
func (es Errors) ErrOrNil() error {
	if len(es) == 0 {
		return nil
	}
	return es
}
