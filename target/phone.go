package target

import (
	"strings"
)

// NumberAllowed reports whether number matches one of the E.164 patterns
// (digits with an optional leading +, `*` matching any run of digits, `?` one
// digit). Spaces, dashes and dots in the number are ignored. An empty pattern
// list allows nothing: the sip target refuses to dial.
func NumberAllowed(number string, patterns []string) bool {
	n := normalizeNumber(number)
	if n == "" {
		return false
	}
	for _, p := range patterns {
		if globNumber(normalizeNumber(p), n) {
			return true
		}
	}
	return false
}

// Templated reports whether a number still holds a template or variable
// (`{{...}}` or `${...}`), so it cannot be checked before the run.
func Templated(s string) bool {
	return strings.Contains(s, "{{") || strings.Contains(s, "${")
}

func normalizeNumber(s string) string {
	var b strings.Builder
	for i, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9', r == '*', r == '?':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func globNumber(pattern, s string) bool {
	if pattern == "" {
		return s == ""
	}
	switch pattern[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if globNumber(pattern[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		return s != "" && globNumber(pattern[1:], s[1:])
	}
	return s != "" && s[0] == pattern[0] && globNumber(pattern[1:], s[1:])
}
