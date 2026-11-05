package report

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/xavidop/tamesu/internal/model"
)

// Console prints live progress, one PASS/FAIL line per test and, on failure,
// the failing assertions and the last three turns.
type Console struct {
	mu      sync.Mutex
	w       io.Writer
	verbose bool
	st      Style
}

// NewConsole returns a console reporter writing to w, without colours.
// verbose adds a line per run.
func NewConsole(w io.Writer, verbose bool) *Console { return &Console{w: w, verbose: verbose} }

// WithColor colours the verdicts, counts and reasons when enabled.
func (c *Console) WithColor(enabled bool) *Console {
	c.st = NewStyle(enabled)
	return c
}

func (c *Console) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.w, format, args...)
}

// RunStarted prints the header line.
func (c *Console) RunStarted(info RunInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := fmt.Sprintf("%s %s, output in %s", c.st.Bold("tamesu run:"), plural(info.Tests, "test"), c.st.Info(info.OutDir))
	if l := FormatLabels(info.Labels); l != "" {
		line += " · " + c.st.Dim(l)
	}
	c.printf("%s\n", line)
}

// TestStarted prints a START line in verbose mode.
func (c *Console) TestStarted(t *TestOutcome) {
	if !c.verbose {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.printf("%s  %s / %s (%s, %s)\n", c.st.Dim("START"), t.Suite, t.Name, t.Target, plural(t.Repeat, "run"))
}

// RunFinished prints one line per repeat in verbose mode.
func (c *Console) RunFinished(t *TestOutcome, r *RunOutcome) {
	if !c.verbose {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.st.Pass("pass")
	switch {
	case r.ErrorKind != "":
		state = c.st.Fail(r.ErrorKind + ": " + r.Error)
	case !r.Passed:
		state = c.st.Fail("fail")
	}
	c.printf("  run %d/%d  %s  %s\n", r.Index, t.Repeat, fmtDuration(r.Duration), state)
}

// TestFinished prints the verdict and, on failure, the reasons and last turns.
func (c *Console) TestFinished(t *TestOutcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	label := c.st.Pass("PASS ")
	switch {
	case t.Skipped():
		label = c.st.Dim("SKIP ")
	case t.Quarantined():
		label = c.st.Warn("QUAR ")
	case t.Errored():
		label = c.st.Fail("ERROR")
	case !t.Passed:
		label = c.st.Fail("FAIL ")
	}
	var dur time.Duration
	passed := 0
	for _, r := range t.Runs {
		dur += r.Duration
		if r.Passed {
			passed++
		}
	}
	runs := ""
	if t.Repeat > 1 {
		runs = fmt.Sprintf("  (%d/%d runs passed, threshold %.0f%%)", passed, len(t.Runs), t.PassThreshold*100)
	}
	if t.Skipped() {
		c.printf("%s  %s / %s  %s\n", label, t.Suite, t.Name, c.st.Dim(t.SkipReason()))
		return
	}
	c.printf("%s  %s / %s  %s%s\n", label, t.Suite, t.Name, c.st.Dim(fmtDuration(dur)), c.st.Dim(runs))
	if l := t.Load; l != nil {
		c.printf("       load: %d users · %d runs in %s · %.1f runs/s · %.0f%% errors · turn latency p50 %.0fms p95 %.0fms max %.0fms (%d turns)\n",
			l.Users, l.Runs, fmtDuration(time.Duration(l.DurationMs*float64(time.Millisecond))), l.RunsPerSecond, l.ErrorRate*100,
			l.TurnLatencyMs.P50, l.TurnLatencyMs.P95, l.TurnLatencyMs.Max, l.TurnLatencyMs.Count)
	}
	if t.Passed {
		return
	}
	if q := t.Quarantine; q != nil {
		c.printf("       %s\n", c.st.Warn(fmt.Sprintf("quarantined%s: %s", optional(" until ", q.Until), q.Reason)))
	}
	for _, r := range t.Runs {
		if r.Passed {
			continue
		}
		prefix := "   "
		if t.Repeat > 1 {
			c.printf("    run %d:\n", r.Index)
			prefix = "     "
		}
		if r.Error != "" {
			c.printf("%s %s: %s\n", prefix, c.st.Fail(r.ErrorKind), r.Error)
		}
		for _, a := range r.FailedAssertions() {
			c.printf("%s %s %s: %s\n", prefix, c.st.Fail("x"), c.st.Bold(a.Name), a.Reason)
		}
		if n := len(r.Transcript); n > 0 {
			c.printf("%s %s\n", prefix, c.st.Dim("last turns:"))
			start := max(n-3, 0)
			for _, turn := range r.Transcript[start:] {
				c.printf("%s   %s %s\n", prefix, c.st.Dim(fmt.Sprintf("%-6s", turn.Role+":")), c.st.Dim(oneLine(turn.Text, 160)))
			}
		}
		if t.Repeat == 1 {
			break
		}
	}
}

// Finish prints the totals.
func (c *Console) Finish(_ context.Context, s *Summary) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	passed, failed, errored := s.Counts()
	count := func(n int, word string, paint func(string) string) string {
		text := fmt.Sprintf("%d %s", n, word)
		if n > 0 {
			return paint(text)
		}
		return text
	}
	line := "\n" + count(passed, "passed", c.st.Pass) + ", " + count(failed, "failed", c.st.Fail) + ", " + count(errored, "errors", c.st.Fail)
	if q := len(s.Quarantined()); q > 0 {
		line += ", " + c.st.Warn(fmt.Sprintf("%d quarantined", q))
	}
	if n := len(s.Skipped()); n > 0 {
		line += fmt.Sprintf(", %d skipped", n)
	}
	line += " in " + fmtDuration(s.Duration())
	if s.StoppedEarly == StoppedFailFast {
		line += " · stopped after the first failure (--fail-fast)"
	}
	if s.Usage.Calls > 0 {
		line += fmt.Sprintf(" · %d model calls, %d tokens, $%.4f", s.Usage.Calls, s.Usage.Total(), s.Usage.CostUSD) + retriesSuffix(s.Usage)
	}
	if c := s.LLMCache; c != nil && c.Hits+c.Misses > 0 {
		line += fmt.Sprintf(" · llm cache: %d hits, %d misses", c.Hits, c.Misses)
	}
	if s.BudgetExceeded {
		line += " · " + c.st.Fail("BUDGET EXCEEDED: the run was stopped")
	}
	if rt := RedTeamLine(BuildRedTeam(s)); rt != "" {
		line += "\n" + rt
	}
	c.printf("%s\n", line)
	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// FormatDuration prints a duration the way the console does.
func FormatDuration(d time.Duration) string { return fmtDuration(d) }

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		return s[:limit-3] + "..."
	}
	return s
}

// retriesSuffix words the model calls that had to be repeated or handed to a
// fallback model, after the usage figures; empty when none were.
func retriesSuffix(u model.Usage) string {
	var out string
	switch {
	case u.Retries == 1:
		out += ", 1 retry"
	case u.Retries > 1:
		out += fmt.Sprintf(", %d retries", u.Retries)
	}
	if u.Fallbacks > 0 {
		out += ", " + plural(u.Fallbacks, "fallback")
	}
	return out
}
