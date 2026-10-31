package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSummarize(t *testing.T) {
	out := map[string]float64{}
	Summarize(out, TurnLatencyMs, []float64{300, 100, 200, 1000})
	assert.Equal(t, 400.0, out["turn_latency_ms"])
	assert.Equal(t, 200.0, out["turn_latency_ms_p50"])
	assert.Equal(t, 1000.0, out["turn_latency_ms_p95"])
	assert.Equal(t, 1000.0, out["turn_latency_ms_max"])
	Summarize(out, TimeToFirstTokenMs, nil)
	_, ok := out["time_to_first_token_ms"]
	assert.False(t, ok)
	assert.Equal(t, "turn_latency_ms_p95", Percentile(TurnLatencyMs, 95))
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		series []float64
		p      int
		want   float64
	}{
		{"single", []float64{7}, 95, 7},
		{"p50 of five", []float64{1, 2, 3, 4, 5}, 50, 3},
		{"p95 of five", []float64{1, 2, 3, 4, 5}, 95, 5},
		{"p50 of two", []float64{1, 2}, 50, 1},
		{"empty", nil, 50, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, percentile(tt.series, tt.p))
		})
	}
}
