package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuarantineActive(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		q    *Quarantine
		want bool
	}{
		{name: "nil", q: nil, want: false},
		{name: "open-ended", q: &Quarantine{Reason: "r"}, want: true},
		{name: "future", q: &Quarantine{Reason: "r", Until: "2026-12-31"}, want: true},
		{name: "the until day itself", q: &Quarantine{Reason: "r", Until: "2026-11-02"}, want: true},
		{name: "expired yesterday", q: &Quarantine{Reason: "r", Until: "2026-11-01"}, want: false},
		{name: "unparseable", q: &Quarantine{Reason: "r", Until: "soon"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.q.Active(now))
		})
	}
}

func TestValidateQuarantine(t *testing.T) {
	old := validatorNow
	validatorNow = func() time.Time { return time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { validatorNow = old })
	_, errs, err := LoadSuite(filepath.Join("testdata", "invalid", "quarantine-bad.yaml"), LoadOptions{Env: noEnv})
	require.NoError(t, err)
	var msgs []string
	warnings := 0
	for _, e := range errs {
		msgs = append(msgs, e.Path+": "+e.Msg)
		if e.Warning {
			warnings++
		}
	}
	assert.Contains(t, msgs, `tests[0] "no reason".quarantine.reason: quarantine needs a reason (the ticket that tracks the flake)`)
	assert.Contains(t, msgs, `tests[1] "bad date".quarantine.until: until must be a date like 2026-12-31`)
	assert.Contains(t, msgs, `tests[2] "expired".quarantine.until: the quarantine expired on 2026-01-31; the test counts again`)
	assert.Equal(t, 1, warnings, "an expired quarantine is only a warning")
	assert.Equal(t, 3, len(errs), "%s", errs)
}
