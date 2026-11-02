package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// ComparisonFile is the comparison written next to summary.json.
const ComparisonFile = "comparison.json"

// Thresholds decide what counts as a regression against the baseline.
type Thresholds struct {
	// MaxLatencyIncreasePct caps the increase of turn_latency_ms_p95,
	// time_to_first_token_ms_p95 and time_to_first_audio_ms_p95 per test;
	// 0 means no check.
	MaxLatencyIncreasePct float64 `json:"max_latency_increase_pct,omitempty"`
	// MaxCostIncreasePct caps the increase of llm_cost_usd per test; 0 means
	// no check.
	MaxCostIncreasePct float64 `json:"max_cost_increase_pct,omitempty"`
	// FailOnNewFailures makes a test that passed in the baseline and fails
	// now a regression.
	FailOnNewFailures bool `json:"fail_on_new_failures"`
}

// DefaultThresholds are what tamesu uses without a baseline configuration.
var DefaultThresholds = Thresholds{MaxLatencyIncreasePct: 20, MaxCostIncreasePct: 50, FailOnNewFailures: true}

// Growth below these absolute amounts is noise, whatever the percentage.
const (
	MinLatencyIncreaseMs = 50
	MinCostIncreaseUSD   = 0.001
)

// latencyMetrics are the per-test metrics compared under MaxLatencyIncreasePct.
var latencyMetrics = []string{"turn_latency_ms_p95", "time_to_first_token_ms_p95", "time_to_first_audio_ms_p95"}

// Comparison is a run against a baseline.
type Comparison struct {
	BaselineDir string      `json:"baseline_dir,omitempty"`
	BaselineAt  string      `json:"baseline_at,omitempty"`
	Thresholds  Thresholds  `json:"thresholds"`
	NewFailures []string    `json:"new_failures"`
	Fixed       []string    `json:"fixed"`
	Added       []string    `json:"added"`
	Removed     []string    `json:"removed"`
	Tests       []TestDelta `json:"tests"`
	Regressed   bool        `json:"regressed"`
}

// TestDelta is one test in both runs.
type TestDelta struct {
	Suite          string        `json:"suite"`
	Name           string        `json:"name"`
	PassedBefore   bool          `json:"passed_before"`
	PassedAfter    bool          `json:"passed_after"`
	PassRateBefore float64       `json:"pass_rate_before"`
	PassRateAfter  float64       `json:"pass_rate_after"`
	Metrics        []MetricDelta `json:"metrics,omitempty"`
	Regressed      bool          `json:"regressed"`
}

// MetricDelta is one metric of a test in both runs.
type MetricDelta struct {
	Name        string  `json:"name"`
	Before      float64 `json:"before"`
	After       float64 `json:"after"`
	IncreasePct float64 `json:"increase_pct"`
	Regressed   bool    `json:"regressed"`
}

// Compare matches the tests of cur with base by suite and name and applies
// the thresholds.
func Compare(base, cur *Summary, t Thresholds) *Comparison {
	c := &Comparison{Thresholds: t, NewFailures: []string{}, Fixed: []string{}, Added: []string{}, Removed: []string{}}
	if base != nil {
		c.BaselineDir = base.OutDir
		if !base.StartedAt.IsZero() {
			c.BaselineAt = base.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
	}
	key := func(t *TestOutcome) string { return t.Suite + " / " + t.Name }
	before := map[string]*TestOutcome{}
	if base != nil {
		for _, t := range base.Tests {
			before[key(t)] = t
		}
	}
	seen := map[string]bool{}
	for _, t := range cur.Tests {
		k := key(t)
		seen[k] = true
		b, ok := before[k]
		if !ok {
			c.Added = append(c.Added, k)
			continue
		}
		d := TestDelta{Suite: t.Suite, Name: t.Name, PassedBefore: b.Passed, PassedAfter: t.Passed, PassRateBefore: b.PassRate, PassRateAfter: t.PassRate}
		switch {
		case b.Passed && !t.Passed:
			c.NewFailures = append(c.NewFailures, k)
			d.Regressed = d.Regressed || t.FailOnNewFailures(c.Thresholds)
		case !b.Passed && t.Passed:
			c.Fixed = append(c.Fixed, k)
		}
		bm, am := medianMetrics(b), medianMetrics(t)
		for _, name := range latencyMetrics {
			if md, ok := metricDelta(name, bm, am, c.Thresholds.MaxLatencyIncreasePct, MinLatencyIncreaseMs); ok {
				d.Metrics = append(d.Metrics, md)
				d.Regressed = d.Regressed || md.Regressed
			}
		}
		if md, ok := metricDelta("llm_cost_usd", bm, am, c.Thresholds.MaxCostIncreasePct, MinCostIncreaseUSD); ok {
			d.Metrics = append(d.Metrics, md)
			d.Regressed = d.Regressed || md.Regressed
		}
		c.Regressed = c.Regressed || d.Regressed
		c.Tests = append(c.Tests, d)
	}
	for k := range before {
		if !seen[k] {
			c.Removed = append(c.Removed, k)
		}
	}
	sort.Strings(c.Added)
	sort.Strings(c.Removed)
	return c
}

// FailOnNewFailures reports whether a newly failing test is a regression
// under t.
func (*TestOutcome) FailOnNewFailures(t Thresholds) bool { return t.FailOnNewFailures }

// medianMetrics takes each metric's median over a test's completed runs, so
// one slow repeat does not decide.
func medianMetrics(t *TestOutcome) map[string]float64 {
	series := map[string][]float64{}
	for _, r := range t.Runs {
		if r == nil || r.Error != "" {
			continue
		}
		for k, v := range r.Metrics {
			series[k] = append(series[k], v)
		}
	}
	out := map[string]float64{}
	for k, vs := range series {
		sort.Float64s(vs)
		out[k] = vs[len(vs)/2]
	}
	return out
}

func metricDelta(name string, before, after map[string]float64, maxPct, minAbs float64) (MetricDelta, bool) {
	b, okB := before[name]
	a, okA := after[name]
	if !okB || !okA {
		return MetricDelta{}, false
	}
	d := MetricDelta{Name: name, Before: b, After: a}
	switch {
	case b > 0:
		d.IncreasePct = (a - b) / b * 100
	case a > 0:
		d.IncreasePct = 100
	}
	d.Regressed = maxPct > 0 && d.IncreasePct > maxPct && a-b >= minAbs
	return d, true
}

// WriteComparison writes comparison.json into dir.
func WriteComparison(dir string, c *Comparison) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ComparisonFile), append(b, '\n'), 0o644)
}

// PrintComparison writes the comparison for a terminal.
func PrintComparison(w io.Writer, c *Comparison) {
	head := "baseline"
	if c.BaselineAt != "" {
		head += " " + c.BaselineAt
	}
	verdict := "no regression"
	if c.Regressed {
		verdict = "REGRESSED"
	}
	fmt.Fprintf(w, "\nvs %s: %s · %d compared, %d new failures, %d fixed, %d added, %d removed\n", head, verdict, len(c.Tests), len(c.NewFailures), len(c.Fixed), len(c.Added), len(c.Removed))
	for _, k := range c.NewFailures {
		fmt.Fprintf(w, "  new failure  %s\n", k)
	}
	for _, k := range c.Fixed {
		fmt.Fprintf(w, "  fixed        %s\n", k)
	}
	for _, d := range c.Tests {
		for _, m := range d.Metrics {
			if m.Regressed {
				fmt.Fprintf(w, "  regression   %s / %s: %s %s → %s (+%.0f%%, limit %.0f%%)\n", d.Suite, d.Name, m.Name, fmtDelta(m.Name, m.Before), fmtDelta(m.Name, m.After), m.IncreasePct, limitFor(m.Name, c.Thresholds))
			}
		}
	}
}

func limitFor(name string, t Thresholds) float64 {
	if name == "llm_cost_usd" {
		return t.MaxCostIncreasePct
	}
	return t.MaxLatencyIncreasePct
}

func fmtDelta(name string, v float64) string {
	if name == "llm_cost_usd" {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("%.0f ms", v)
}
