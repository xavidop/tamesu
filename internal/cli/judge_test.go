package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

const calibrationLabels = `
- name: states the policy
  rubric: "should pass: the agent states the 30-day policy"
  expected: pass
  transcript:
    - { role: user, text: "Can I return shoes from 40 days ago?" }
    - { role: agent, text: "Our window is 30 days." }
- name: judge is too lenient here
  rubric: "should pass: the agent states the 30-day policy"
  expected: fail
  transcript:
    - { role: user, text: "Can I return shoes from 40 days ago?" }
    - { role: agent, text: "Sure, any time." }
- name: never promises a refund
  rubric: "the agent never promises a refund"
  expected: fail
  transcript:
    - { role: user, text: "Refund me" }
    - { role: agent, text: "Done, refunded." }
`

func TestJudgeCalibrate(t *testing.T) {
	deps, _, _ := runDeps(t)
	dir := writeProject(t, map[string]string{"labels.yaml": calibrationLabels})
	cfg := filepath.Join(dir, "tamesu.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(fmt.Sprintf("version: 1\nsuites: [\"s/**/*.yaml\"]\ncache: { dir: %q }\n", filepath.Join(dir, "cache"))), 0o644))
	labels := filepath.Join(dir, "labels.yaml")
	out := filepath.Join(dir, "report", "calibration.json")

	// The fake judge passes whenever the rubric mentions "pass": one false pass.
	code, stdout, _ := run(t, nil, deps, "judge", "calibrate", labels, "--config", cfg, "--out", out)
	assert.Equal(t, cli.ExitFailed, code, stdout)
	assert.Contains(t, stdout, "agreement 67% (2 of 3 labels), 1 false pass, 0 false fail, 0 errors · minimum 80%: BELOW THE MINIMUM")
	assert.Contains(t, stdout, "x judge is too lenient here: expected fail, judge said pass · judged")
	assert.Contains(t, stdout, "llm cache: 0 hits, 3 misses")
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	var rep struct {
		Agreement float64 `json:"agreement"`
		Rubrics   []struct {
			Rubric    string  `json:"rubric"`
			Agreement float64 `json:"agreement"`
		} `json:"rubrics"`
	}
	require.NoError(t, json.Unmarshal(b, &rep))
	assert.InDelta(t, 2.0/3, rep.Agreement, 1e-9)
	require.Len(t, rep.Rubrics, 2)
	assert.Equal(t, "should pass: the agent states the 30-day policy", rep.Rubrics[0].Rubric, "weakest rubric first")

	// A lower bar passes, and the second run is answered from the cache.
	code, stdout, _ = run(t, nil, deps, "judge", "calibrate", labels, "--config", cfg, "--min-agreement", "0.6")
	assert.Equal(t, cli.ExitOK, code, stdout)
	assert.Contains(t, stdout, "minimum 60%: OK")
	assert.Contains(t, stdout, "llm cache: 3 hits, 0 misses")

	code, _, stderr := run(t, nil, deps, "judge", "calibrate", labels, "--config", cfg, "--min-agreement", "2")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "--min-agreement is a fraction")
	code, _, stderr = run(t, nil, deps, "judge", "calibrate", filepath.Join(dir, "missing.yaml"), "--config", cfg)
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "missing.yaml")
	noAI := deps
	noAI.NewProvider = nil
	code, _, stderr = run(t, nil, noAI, "judge", "calibrate", labels, "--config", cfg)
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "no model provider")

	code, stdout, _ = run(t, nil, deps, "judge")
	assert.Equal(t, cli.ExitOK, code)
	assert.Contains(t, stdout, "calibrate")
}
