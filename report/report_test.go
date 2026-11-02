package report

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/model"
)

func sampleSummary() *Summary {
	t0 := time.Date(2026, 10, 31, 14, 0, 0, 0, time.UTC)
	ok := &TestOutcome{Suite: "support", Name: "refund", Target: "api", Type: "rest", Repeat: 1, PassThreshold: 1, StartedAt: t0, Runs: []*RunOutcome{{
		Index: 1, Passed: true, StartedAt: t0, Duration: 1200 * time.Millisecond,
		Assertions: []tassert.Result{{Name: "judge", Pass: true, Reason: "ok"}},
		Metrics:    map[string]float64{"turns": 2},
	}}}
	ok.Finalize(t0.Add(2 * time.Second))
	bad := &TestOutcome{Suite: "support", Name: "booking", Target: "voice", Type: "livekit", Repeat: 2, PassThreshold: 1, StartedAt: t0, Runs: []*RunOutcome{
		{Index: 1, Passed: true, Duration: time.Second},
		{Index: 2, Passed: false, Duration: 2 * time.Second,
			Assertions: []tassert.Result{{Name: `judge "confirmed"`, Pass: false, Reason: "the agent invented availability"}, {Name: "turns <= 6", Pass: true, Reason: "6 turns"}},
			Transcript: []tassert.Turn{{Role: "user", Text: "Move it to Saturday"}, {Role: "agent", Text: "Sure, Sunday works", Index: 1}}},
	}}
	bad.Finalize(t0.Add(5 * time.Second))
	broken := &TestOutcome{Suite: "other", Name: "ivr", Target: "phone", Type: "sip", Repeat: 1, PassThreshold: 1, StartedAt: t0, Runs: []*RunOutcome{{Index: 1, Error: "LiveKit unreachable", ErrorKind: KindInfra}}}
	broken.Finalize(t0.Add(time.Second))
	return &Summary{StartedAt: t0, FinishedAt: t0.Add(6 * time.Second), OutDir: "/out", Tests: []*TestOutcome{ok, bad, broken}, Usage: model.Usage{Calls: 7, InputTokens: 3000, OutputTokens: 210, CostUSD: 0.0123}}
}

func TestSummaryCountsAndExitCode(t *testing.T) {
	s := sampleSummary()
	passed, failed, errored := s.Counts()
	assert.Equal(t, []int{1, 1, 1}, []int{passed, failed, errored})
	assert.Equal(t, 3, s.ExitCode(), "an infrastructure error wins over a failure")
	s.Tests = s.Tests[:2]
	assert.Equal(t, 1, s.ExitCode())
	s.Tests = s.Tests[:1]
	assert.Equal(t, 0, s.ExitCode())
	s.BudgetExceeded = true
	assert.Equal(t, 4, s.ExitCode())
}

func TestFinalizeThreshold(t *testing.T) {
	to := &TestOutcome{Repeat: 5, PassThreshold: 0.8, Runs: []*RunOutcome{{Passed: true}, {Passed: true}, {Passed: true}, {Passed: true}, {Passed: false}}}
	to.Finalize(time.Now())
	assert.True(t, to.Passed)
	assert.Equal(t, 0.8, to.PassRate)
	to.PassThreshold = 1
	to.Finalize(time.Now())
	assert.False(t, to.Passed)
}

func TestConsole(t *testing.T) {
	var buf bytes.Buffer
	c := NewConsole(&buf, false)
	s := sampleSummary()
	c.RunStarted(RunInfo{OutDir: "/out", Tests: 3})
	for _, to := range s.Tests {
		c.TestStarted(to)
		for _, r := range to.Runs {
			c.RunFinished(to, r)
		}
		c.TestFinished(to)
	}
	require.NoError(t, c.Finish(context.Background(), s))
	out := buf.String()
	assert.Contains(t, out, "tamesu run: 3 tests, output in /out\n")
	assert.Contains(t, out, "PASS   support / refund  1.2s\n")
	assert.Contains(t, out, "FAIL   support / booking  3.0s  (1/2 runs passed, threshold 100%)\n")
	assert.Contains(t, out, `x judge "confirmed": the agent invented availability`)
	assert.Contains(t, out, "agent: Sure, Sunday works")
	assert.NotContains(t, out, "turns <= 6", "passing assertions are not listed")
	assert.Contains(t, out, "ERROR  other / ivr")
	assert.Contains(t, out, "infra: LiveKit unreachable")
	assert.Contains(t, out, "1 passed, 1 failed, 1 errors in 6.0s · 7 model calls, 3210 tokens, $0.0123\n")
	assert.NotContains(t, out, "START", "verbose lines are off by default")

	buf.Reset()
	NewConsole(&buf, true).TestStarted(s.Tests[0])
	assert.Contains(t, buf.String(), "START  support / refund (api, 1 run)")
}

func TestJSON(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, NewJSON(dir).Finish(context.Background(), sampleSummary()))
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	require.NoError(t, err)
	var got Summary
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Len(t, got.Tests, 3)
	assert.Equal(t, "refund", got.Tests[0].Name)
	assert.Equal(t, 0.0123, got.Usage.CostUSD)
}

func TestJUnit(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, NewJUnit(dir).Finish(context.Background(), sampleSummary()))
	b, err := os.ReadFile(filepath.Join(dir, "junit.xml"))
	require.NoError(t, err)
	var got junitSuites
	require.NoError(t, xml.Unmarshal(b, &got))
	assert.Equal(t, 3, got.Tests)
	assert.Equal(t, 1, got.Failures)
	assert.Equal(t, 1, got.Errors)
	require.Len(t, got.Suites, 2)
	support := got.Suites[0]
	assert.Equal(t, "support", support.Name)
	require.Len(t, support.Cases, 2)
	assert.Nil(t, support.Cases[0].Failure)
	booking := support.Cases[1]
	require.NotNil(t, booking.Failure)
	assert.Equal(t, "1 of 2 runs passed (threshold 100%)", booking.Failure.Message)
	assert.Contains(t, booking.Failure.Body, `judge "confirmed": the agent invented availability`)
	assert.Contains(t, booking.SystemOut, "agent: Sure, Sunday works")
	require.NotNil(t, got.Suites[1].Cases[0].Error)
	assert.Equal(t, "infra: LiveKit unreachable", got.Suites[1].Cases[0].Error.Message)
}

func TestMulti(t *testing.T) {
	var a, b bytes.Buffer
	m := Multi{NewConsole(&a, false), NewConsole(&b, false)}
	s := sampleSummary()
	m.RunStarted(RunInfo{Tests: 3})
	m.TestStarted(s.Tests[0])
	m.RunFinished(s.Tests[0], s.Tests[0].Runs[0])
	m.TestFinished(s.Tests[0])
	require.NoError(t, m.Finish(context.Background(), s))
	assert.Equal(t, a.String(), b.String())
	assert.Contains(t, a.String(), "PASS   support / refund")
}

func TestComputeLoad(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	turns := func(latencies ...time.Duration) []tassert.Turn {
		var out []tassert.Turn
		at := t0
		for i, l := range latencies {
			out = append(out, tassert.Turn{Role: "user", Text: "u", At: at}, tassert.Turn{Role: "agent", Text: "a", At: at.Add(l), Index: i + 1})
			at = at.Add(l + time.Second)
		}
		return out
	}
	to := &TestOutcome{StartedAt: t0, FinishedAt: t0.Add(4 * time.Second), Runs: []*RunOutcome{
		{Passed: true, Transcript: turns(100*time.Millisecond, 200*time.Millisecond)},
		{Passed: false, Transcript: turns(300 * time.Millisecond)},
		{Passed: true, Transcript: turns(400*time.Millisecond, 1000*time.Millisecond)},
		{Passed: true, ErrorKind: KindInfra},
	}}
	ls := ComputeLoad(to, 2)
	assert.Equal(t, 2, ls.Users)
	assert.Equal(t, 4, ls.Runs)
	assert.Equal(t, 4000.0, ls.DurationMs)
	assert.Equal(t, 1.0, ls.RunsPerSecond)
	assert.Equal(t, 0.25, ls.ErrorRate)
	assert.Equal(t, 5, ls.TurnLatencyMs.Count)
	assert.Equal(t, 300.0, ls.TurnLatencyMs.P50)
	assert.Equal(t, 1000.0, ls.TurnLatencyMs.Max)
	h := ls.TurnLatencyMs.Histogram
	require.Len(t, h, len(LatencyEdges)+1)
	counts := map[float64]int{}
	for _, b := range h {
		counts[b.UpToMs] = b.Count
	}
	assert.Equal(t, map[float64]int{50: 0, 100: 1, 200: 1, 300: 1, 500: 1, 750: 0, 1000: 1, 1500: 0, 2000: 0, 3000: 0, 5000: 0, 0: 0}, counts,
		"100 and 200 ms sit on their upper bounds, 400 falls in 300–500, 1000 in 750–1000")
	assert.Nil(t, Histogram(nil))
	over := Histogram([]float64{6000, 49.9})
	assert.Equal(t, 1, over[len(over)-1].Count, "above the last edge goes to the overflow bucket")
	assert.Equal(t, 1, over[0].Count)
	assert.GreaterOrEqual(t, ls.TurnLatencyMs.P95, 400.0)

	empty := ComputeLoad(&TestOutcome{}, 1)
	assert.Equal(t, 0, empty.Runs)
	assert.Equal(t, 0.0, empty.ErrorRate)
}
