package report

import (
	"bytes"
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// quarantine marks the failed "booking" test of sampleSummary as quarantined.
func quarantinedSummary() *Summary {
	s := sampleSummary()
	s.Tests[1].Quarantine = &Quarantine{Reason: "SUP-412 flaky since the payments migration", Until: "2026-12-31"}
	return s
}

func TestQuarantinedCountsAndExitCode(t *testing.T) {
	s := quarantinedSummary()
	passed, failed, errored := s.Counts()
	assert.Equal(t, []int{1, 0, 1}, []int{passed, failed, errored})
	require.Len(t, s.Quarantined(), 1)
	assert.Equal(t, "booking", s.Quarantined()[0].Name)
	s.Tests = s.Tests[:2]
	assert.Equal(t, 0, s.ExitCode(), "a quarantined failure does not fail the run")

	ok := &TestOutcome{Passed: true, Quarantine: &Quarantine{Reason: "r"}}
	assert.False(t, ok.Quarantined(), "a quarantined test that passes just passes")
	passed, _, _ = (&Summary{Tests: []*TestOutcome{ok}}).Counts()
	assert.Equal(t, 1, passed)
}

func TestQuarantinedConsole(t *testing.T) {
	var buf bytes.Buffer
	c := NewConsole(&buf, false)
	s := quarantinedSummary()
	c.TestFinished(s.Tests[1])
	require.NoError(t, c.Finish(context.Background(), &Summary{Tests: s.Tests[:2], StartedAt: s.StartedAt, FinishedAt: s.FinishedAt}))
	out := buf.String()
	assert.Contains(t, out, "QUAR   support / booking")
	assert.Contains(t, out, "quarantined until 2026-12-31: SUP-412 flaky since the payments migration")
	assert.Contains(t, out, "the agent invented availability", "the failure is still shown")
	assert.Contains(t, out, "1 passed, 0 failed, 0 errors, 1 quarantined in")
}

func TestQuarantinedJUnit(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, NewJUnit(dir).Finish(context.Background(), quarantinedSummary()))
	b, err := os.ReadFile(filepath.Join(dir, "junit.xml"))
	require.NoError(t, err)
	var got junitSuites
	require.NoError(t, xml.Unmarshal(b, &got))
	assert.Equal(t, 3, got.Tests)
	assert.Equal(t, 0, got.Failures)
	assert.Equal(t, 1, got.Errors)
	assert.Equal(t, 1, got.Skipped)
	booking := got.Suites[0].Cases[1]
	assert.Nil(t, booking.Failure)
	require.NotNil(t, booking.Skipped)
	assert.Equal(t, "quarantined until 2026-12-31: SUP-412 flaky since the payments migration", booking.Skipped.Message)
	assert.Contains(t, booking.Skipped.Body, "the agent invented availability")
}

func TestQuarantinedMarkdownSlackGitHub(t *testing.T) {
	s := quarantinedSummary()
	s.Tests = s.Tests[:2]
	s.Tests[1].File = "tests/support.yaml"
	s.Tests[1].Line = 12
	md := RenderMarkdown(s)
	assert.Contains(t, md, "## ✅ tamesu: 1 passed, 0 failed, 0 errors, 1 quarantined")
	assert.Contains(t, md, "| 🚧 | booking |")
	assert.Contains(t, md, "**support / booking** (quarantined until 2026-12-31: SUP-412 flaky since the payments migration): judge \"confirmed\": the agent invented availability")

	text := SlackText(s, "")
	assert.True(t, strings.HasPrefix(text, ":white_check_mark: *tamesu*: 1 passed, 0 failed, 0 errors, 1 quarantined in"), text)
	assert.Contains(t, text, "• *support / booking* (quarantined until 2026-12-31: SUP-412 flaky since the payments migration): judge")

	ann := Annotations(s)
	require.Len(t, ann, 1)
	assert.True(t, strings.HasPrefix(ann[0], "::warning file=tests/support.yaml,line=12,title=tamesu%3A booking::(quarantined until 2026-12-31: SUP-412 flaky since the payments migration) judge"), ann[0])

	var html bytes.Buffer
	require.NoError(t, RenderHTML(&html, s, t.TempDir()))
	assert.Contains(t, html.String(), `<b>1</b>quarantined`)
	assert.Contains(t, html.String(), `<span class="badge quarantined">quarantined</span>`)
	assert.Contains(t, html.String(), "quarantined until 2026-12-31: SUP-412 flaky since the payments migration")
}

func TestQuarantinedIsNotARegression(t *testing.T) {
	base := summaryWith(outcome("s", "flaky", true), outcome("s", "solid", true))
	flaky := outcome("s", "flaky", false)
	flaky.Quarantine = &Quarantine{Reason: "SUP-1"}
	cur := &Summary{StartedAt: time.Now(), Tests: []*TestOutcome{flaky, outcome("s", "solid", true)}}
	c := Compare(base, cur, DefaultThresholds)
	assert.Equal(t, []string{"s / flaky"}, c.NewFailures, "listed")
	assert.False(t, c.Regressed, "but not a regression")
}

func TestSkippedTests(t *testing.T) {
	s := sampleSummary()
	skipped := &TestOutcome{Suite: "support", Name: "never ran", Target: "api", Type: "rest", Repeat: 2, PassThreshold: 1, StartedAt: s.StartedAt, Runs: []*RunOutcome{
		{Index: 1, ErrorKind: KindSkipped, Error: "the run stopped after a failure (--fail-fast)"},
		{Index: 2, ErrorKind: KindSkipped, Error: "the run stopped after a failure (--fail-fast)"},
	}}
	skipped.Finalize(s.StartedAt)
	assert.True(t, skipped.Skipped())
	assert.False(t, skipped.Passed)
	assert.False(t, skipped.Errored())
	skipped.Quarantine = &Quarantine{Reason: "r"}
	assert.False(t, skipped.Quarantined(), "a skipped test is not a quarantined failure")
	skipped.Quarantine = nil
	s.Tests = append(s.Tests[:2], skipped)
	s.StoppedEarly = StoppedFailFast
	passed, failed, errored := s.Counts()
	assert.Equal(t, []int{1, 1, 0}, []int{passed, failed, errored})
	require.Len(t, s.Skipped(), 1)
	assert.Equal(t, 1, s.ExitCode())

	partial := &TestOutcome{Repeat: 2, PassThreshold: 1, Runs: []*RunOutcome{{Passed: true}, {ErrorKind: KindSkipped}}}
	partial.Finalize(time.Now())
	assert.False(t, partial.Skipped())
	assert.True(t, partial.Passed, "the completed run decides")
	assert.Equal(t, 1.0, partial.PassRate)

	var buf bytes.Buffer
	c := NewConsole(&buf, false)
	c.TestFinished(skipped)
	require.NoError(t, c.Finish(context.Background(), s))
	assert.Contains(t, buf.String(), "SKIP   support / never ran  the run stopped after a failure (--fail-fast)")
	assert.Contains(t, buf.String(), "1 passed, 1 failed, 0 errors, 1 skipped in")
	assert.Contains(t, buf.String(), "stopped after the first failure (--fail-fast)")

	dir := t.TempDir()
	require.NoError(t, NewJUnit(dir).Finish(context.Background(), s))
	b, err := os.ReadFile(filepath.Join(dir, "junit.xml"))
	require.NoError(t, err)
	var got junitSuites
	require.NoError(t, xml.Unmarshal(b, &got))
	assert.Equal(t, 1, got.Skipped)
	assert.Equal(t, 1, got.Failures)
	require.NotNil(t, got.Suites[0].Cases[2].Skipped)
	assert.Contains(t, got.Suites[0].Cases[2].Skipped.Message, "--fail-fast")

	md := RenderMarkdown(s)
	assert.Contains(t, md, "## ❌ tamesu: 1 passed, 1 failed, 0 errors, 1 skipped")
	assert.Contains(t, md, "stopped after the first failure (`--fail-fast`)")
	assert.Contains(t, md, "| ⏭️ | never ran |")
	assert.NotContains(t, md, "**support / never ran**", "skipped tests are not failures")
	assert.Contains(t, SlackText(s, ""), "1 skipped in")
	assert.Len(t, Annotations(s), 1)
	var html bytes.Buffer
	require.NoError(t, RenderHTML(&html, s, dir))
	assert.Contains(t, html.String(), `<span class="badge skipped">skipped</span>`)
	assert.Contains(t, html.String(), `<b>1</b>skipped`)

	base := summaryWith(outcome("support", "never ran", true))
	c2 := Compare(base, s, DefaultThresholds)
	assert.Empty(t, c2.NewFailures, "a skipped test is not a new failure")
}
