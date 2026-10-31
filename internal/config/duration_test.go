package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDurationUnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr string
	}{
		{name: "minutes", in: "3m", want: 3 * time.Minute},
		{name: "millis", in: "250ms", want: 250 * time.Millisecond},
		{name: "compound", in: "1h30m", want: 90 * time.Minute},
		{name: "empty", in: `""`, want: 0},
		{name: "words", in: `"3 minutes"`, wantErr: `invalid duration "3 minutes"`},
		{name: "bare number", in: "30", wantErr: `invalid duration "30"`},
		{name: "list", in: "[3m]", wantErr: "a duration must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			err := yaml.Unmarshal([]byte(tt.in), &d)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, d.Std())
		})
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	b, err := json.Marshal(Duration(90 * time.Second))
	require.NoError(t, err)
	assert.Equal(t, `"1m30s"`, string(b))
	var d Duration
	require.NoError(t, json.Unmarshal(b, &d))
	assert.Equal(t, 90*time.Second, d.Std())
}
