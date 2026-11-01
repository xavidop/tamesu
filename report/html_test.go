package report

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

func htmlSampleSummary(dir string) *Summary {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	voiceDir := filepath.Join(dir, "tests", "refund-by-voice", "run-1")
	webDir := filepath.Join(dir, "tests", "checkout", "run-1")
	return &Summary{
		StartedAt: start, FinishedAt: start.Add(12 * time.Second), OutDir: dir, Version: "1.0.0",
		Usage: model.Usage{Calls: 7, InputTokens: 4000, OutputTokens: 300, CostUSD: 0.0123},
		Tests: []*TestOutcome{
			{Suite: "voice", Name: "refund by voice", Target: "phone", Type: "sip", Repeat: 1, PassThreshold: 1, Passed: false, Tags: []string{"voice"}, Runs: []*RunOutcome{{
				Index: 1, Attempt: 1, Attempts: 1, Passed: false, Duration: 6 * time.Second, Dir: voiceDir,
				Assertions: []tassert.Result{
					{Name: `judge "states the policy"`, Pass: false, Reason: "the agent promised an exception", Evidence: []string{"sure, 40 days is fine"}, TraceID: "trace-judge"},
					{Name: "metric time_to_first_audio_ms_p95 < 1500", Pass: true, Reason: "720 < 1500"},
				},
				Transcript: []tassert.Turn{{Role: "user", Text: "Can I return shoes from 40 days ago?"}, {Role: "agent", Text: "Sure, 40 days is fine.", Index: 1}},
				Metrics:    map[string]float64{"time_to_first_audio_ms": 720, "talk_over_ratio": 0.0312, "turns": 1},
				Events: []target.Event{
					{At: start, Kind: "joined", Detail: "tamesu-sip-1"},
					{At: start.Add(1500 * time.Millisecond), Kind: "user_speech_start"},
					{At: start.Add(3200 * time.Millisecond), Kind: "agent_speech_start"},
					{At: start.Add(4 * time.Second), Kind: "dtmf_received", Detail: "9"},
				},
				Artifacts: map[string]string{"audio.wav": "audio.wav"},
				TraceIDs:  []string{"trace-sim", "trace-judge"},
				StopReason: "simulator: done",
			}}},
			{Suite: "web", Name: "checkout", Target: "web", Type: "browser", Repeat: 2, PassThreshold: 0.5, Passed: true, Runs: []*RunOutcome{{
				Index: 1, Attempt: 1, Attempts: 1, Passed: true, Duration: 3 * time.Second, Dir: webDir,
				Assertions: []tassert.Result{{Name: "metric goal_steps <= 12", Pass: true, Reason: "6 <= 12"}},
				Steps: []StepRecord{
					{Index: 1, Kind: "goal", Text: "Log in as marta", Success: true, Reason: "the page greets marta", Steps: 3, Screenshot: "screenshots/step-01.png"},
					{Index: 2, Kind: "action", Text: `{"assert_text":"1 item"}`, Success: true, Reason: `text contains "1 item"`, Screenshot: "screenshots/step-02.png"},
				},
				Artifacts: map[string]string{"screenshots/step-01.png": "screenshots/step-01.png", "screenshots/step-02.png": "screenshots/step-02.png", "trace.har": "trace.har"},
				Metrics:   map[string]float64{"goal_steps": 6},
			}, {
				Index: 2, Attempt: 2, Attempts: 2, Passed: false, Duration: 2 * time.Second, Dir: filepath.Join(dir, "tests", "checkout", "run-2"),
				Error: "step 1 (goal): browser closed", ErrorKind: KindInfra,
			}}},
		},
	}
}

func TestRenderHTML(t *testing.T) {
	dir := t.TempDir()
	s := htmlSampleSummary(dir)
	var buf bytes.Buffer
	require.NoError(t, RenderHTML(&buf, s, dir))
	html := buf.String()
	assert.Contains(t, html, "<title>tamesu report · 0 passed, 1 failed, 1 errors</title>")
	assert.Contains(t, html, `class="badge fail">fail`)
	assert.Contains(t, html, `class="badge error">error`, "a run with an infra error marks the test errored")
	assert.Contains(t, html, "the agent promised an exception")
	assert.Contains(t, html, "sure, 40 days is fine")
	assert.Contains(t, html, `src="tests/refund-by-voice/run-1/audio.wav"`, "artifacts are linked relative to the report")
	assert.Contains(t, html, `seek('t0-r0',  1.5 )">caller 1.5s`, "speech events become audio markers")
	assert.Contains(t, html, `>agent 3.2s`)
	assert.Contains(t, html, `dtmf_received 4.0s`)
	assert.Contains(t, html, `<code>3.20s</code> agent_speech_start`)
	assert.Contains(t, html, `<img src="tests/checkout/run-1/screenshots/step-01.png" alt="1 goal: Log in as marta"`)
	assert.Contains(t, html, `href="tests/checkout/run-1/trace.har"`)
	assert.Contains(t, html, "<td>talk_over_ratio</td><td>0.0312</td>")
	assert.Contains(t, html, "<td>time_to_first_audio_ms</td><td>720</td>")
	assert.Contains(t, html, "browser closed")
	assert.Contains(t, html, "trace-judge")
	assert.Contains(t, html, "1/2 runs, threshold 50%")
	assert.NotContains(t, html, "<script src=", "self-contained: no external scripts")
}

func TestWriteAndLoad(t *testing.T) {
	dir := t.TempDir()
	s := htmlSampleSummary(dir)
	require.NoError(t, NewJSON(dir).Finish(t.Context(), s))
	require.NoError(t, NewHTML(dir).Finish(t.Context(), s))
	_, err := os.Stat(filepath.Join(dir, HTMLFile))
	require.NoError(t, err)
	loaded, err := LoadSummary(dir)
	require.NoError(t, err)
	assert.Len(t, loaded.Tests, 2)
	assert.Equal(t, "1.0.0", loaded.Version)
	_, err = LoadSummary(t.TempDir())
	assert.ErrorContains(t, err, "has no summary.json")
	assert.Equal(t, "3", fmtMetric(3))
	assert.Equal(t, "0.5", fmtMetric(0.5))
	assert.Equal(t, "0.00s", fmtOffset(-time.Second))
}
