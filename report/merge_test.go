package report

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeRun writes a run directory with a summary and one artifact per run.
func writeRun(t *testing.T, root, name string, started time.Time, labels map[string]string, tests ...*TestOutcome) string {
	t.Helper()
	dir := filepath.Join(root, name)
	for _, to := range tests {
		to.Dir = filepath.Join(dir, "tests", slugOf(to.Name))
		for i, r := range to.Runs {
			r.Index = i + 1
			r.Dir = filepath.Join(to.Dir, "run-"+string(rune('1'+i)))
			require.NoError(t, os.MkdirAll(r.Dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(r.Dir, "transcript.json"), []byte(`{"turns":[]}`), 0o644))
			r.Artifacts = map[string]string{"transcript.json": "transcript.json"}
		}
		to.Finalize(started.Add(time.Second))
	}
	s := &Summary{StartedAt: started, FinishedAt: started.Add(2 * time.Second), OutDir: dir, Labels: labels, Tests: tests}
	require.NoError(t, NewJSON(dir).Finish(context.Background(), s))
	return dir
}

func passRun() *RunOutcome {
	return &RunOutcome{Passed: true, Duration: time.Second, Metrics: map[string]float64{"turn_latency_ms_p95": 800}}
}
func failRun() *RunOutcome {
	return &RunOutcome{Passed: false, Duration: time.Second, Metrics: map[string]float64{"turn_latency_ms_p95": 1200}}
}

func TestMergeShardsAndRepeats(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	shard1 := writeRun(t, root, "shard-1", t0, map[string]string{"shard": "1/2", "env": "ci"},
		&TestOutcome{Suite: "s", Name: "a", Repeat: 1, PassThreshold: 1, Runs: []*RunOutcome{passRun()}},
		&TestOutcome{Suite: "s", Name: "c", Repeat: 1, PassThreshold: 1, Runs: []*RunOutcome{failRun()}},
	)
	shard2 := writeRun(t, root, "shard-2", t0.Add(time.Minute), map[string]string{"shard": "2/2", "env": "ci"},
		&TestOutcome{Suite: "s", Name: "b", Repeat: 1, PassThreshold: 1, Runs: []*RunOutcome{passRun()}},
	)
	rerun := writeRun(t, root, "rerun", t0.Add(2*time.Minute), nil,
		&TestOutcome{Suite: "s", Name: "c", Repeat: 1, PassThreshold: 1, Runs: []*RunOutcome{passRun()}},
	)
	out := filepath.Join(root, "merged")
	m, err := Merge([]string{shard1, shard2, rerun}, out)
	require.NoError(t, err)
	require.Len(t, m.Tests, 3)
	names := []string{m.Tests[0].Name, m.Tests[1].Name, m.Tests[2].Name}
	assert.Equal(t, []string{"a", "c", "b"}, names, "first-seen order")
	c := m.Tests[1]
	assert.Equal(t, 2, len(c.Runs), "the rerun's run joins the shard's run")
	assert.Equal(t, 2, c.Repeat)
	assert.Equal(t, 0.5, c.PassRate)
	assert.False(t, c.Passed, "threshold 1 over two runs")
	assert.Equal(t, filepath.Join(out, "tests", "c", "run-2"), c.Runs[1].Dir)
	_, err = os.Stat(filepath.Join(out, "tests", "c", "run-2", "transcript.json"))
	require.NoError(t, err, "run directories are copied")
	assert.Equal(t, map[string]string{"env": "ci", "shard": "1/2,2/2"}, m.Labels)
	assert.Equal(t, t0, m.StartedAt)
	assert.Equal(t, t0.Add(2*time.Minute+2*time.Second), m.FinishedAt)
	for _, f := range []string{"summary.json", "junit.xml", "report.html", MarkdownFile} {
		_, err := os.Stat(filepath.Join(out, f))
		assert.NoError(t, err, f)
	}
	loaded, err := LoadSummary(out)
	require.NoError(t, err)
	passed, failed, _ := loaded.Counts()
	assert.Equal(t, []int{2, 1}, []int{passed, failed})

	_, err = Merge(nil, out)
	assert.Error(t, err)
	_, err = Merge([]string{filepath.Join(root, "nope")}, out)
	assert.Error(t, err)
	_, err = Merge([]string{out}, out)
	assert.ErrorContains(t, err, "is the output directory")
}

func TestLoadHistoryAndReport(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	results := [][]bool{{true, true}, {false, true}, {true, true}, {false, true}, {true, true}}
	for i, r := range results {
		a := &TestOutcome{Suite: "s", Name: "a", Repeat: 1, PassThreshold: 1, Runs: []*RunOutcome{passRun()}}
		if !r[0] {
			a.Runs = []*RunOutcome{failRun()}
		}
		b := &TestOutcome{Suite: "s", Name: "b", Repeat: 1, PassThreshold: 1, Runs: []*RunOutcome{passRun()}}
		b.Runs[0].Metrics["turn_latency_ms_p95"] = float64(500 + 100*i)
		writeRun(t, root, t0.Add(time.Duration(i)*time.Hour).Format("2006-01-02T15-04-05"), t0.Add(time.Duration(i)*time.Hour), nil, a, b)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "stray.txt"), nil, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "empty"), 0o755))

	h, err := LoadHistory(root, 0, "")
	require.NoError(t, err)
	require.Len(t, h.Runs, 5)
	require.Len(t, h.Tests, 2)
	a, b := h.Tests[0], h.Tests[1]
	assert.Equal(t, "a", a.Name)
	assert.Equal(t, "✓✗✓✗✓", a.Dots())
	assert.Equal(t, 4, a.Flips)
	assert.True(t, a.Flaky())
	assert.InDelta(t, 0.6, a.PassRate, 1e-9)
	assert.Equal(t, 1, a.Streak)
	assert.False(t, b.Flaky())
	assert.Equal(t, 5, b.Streak)
	assert.Equal(t, 500.0, b.P95First)
	assert.Equal(t, 900.0, b.P95Last)

	last2, err := LoadHistory(root, 2, "")
	require.NoError(t, err)
	assert.Len(t, last2.Runs, 2)
	assert.Equal(t, "✗✓", last2.Tests[0].Dots())

	excl := filepath.Join(root, t0.Add(4*time.Hour).Format("2006-01-02T15-04-05"))
	without, err := LoadHistory(root, 0, excl)
	require.NoError(t, err)
	assert.Len(t, without.Runs, 4)
	assert.True(t, IsRunsDir(excl))
	assert.False(t, IsRunsDir(filepath.Join(root, "out")))

	// The HTML report shows the section once a history has two runs.
	cur, err := LoadSummary(excl)
	require.NoError(t, err)
	hist := HistoryFor(root, 10, excl, cur)
	require.NotNil(t, hist)
	assert.Len(t, hist.Runs, 5)
	var buf strings.Builder
	require.NoError(t, RenderHTMLWithHistory(&buf, cur, excl, hist))
	assert.Contains(t, buf.String(), "History: the last 5 runs")
	assert.Contains(t, buf.String(), `<span class="flaky">flaky</span>`)
	assert.Contains(t, buf.String(), "500 → 900 ms")
	assert.Contains(t, buf.String(), "<polyline points=")
	assert.Nil(t, HistoryFor(t.TempDir(), 10, excl, cur), "no siblings, no history")
}
