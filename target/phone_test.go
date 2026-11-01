package target

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNumberAllowed(t *testing.T) {
	tests := []struct {
		name     string
		number   string
		patterns []string
		ok       bool
	}{
		{"exact", "+18005550100", []string{"+18005550100"}, true},
		{"prefix wildcard", "+18005550100", []string{"+1800555*"}, true},
		{"formatted number", "+1 (800) 555-0100", []string{"+1800555*"}, true},
		{"question marks", "+34910000001", []string{"+3491000000?"}, true},
		{"other prefix", "+34910000001", []string{"+1800555*"}, false},
		{"no patterns", "+18005550100", nil, false},
		{"empty number", "", []string{"*"}, false},
		{"star alone", "+44 20 7946 0000", []string{"*"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.ok, NumberAllowed(tt.number, tt.patterns))
		})
	}
	assert.True(t, Templated("{{vars.number}}"))
	assert.True(t, Templated("${AGENT_NUMBER}"))
	assert.False(t, Templated("+18005550100"))
}
