package report

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Console prints live progress, one PASS/FAIL line per test and, on failure,
// the failing assertions and the last three turns.
type Console struct {
	mu      sync.Mutex
	w       io.Writer
	verbose bool
}

// NewConsole returns a console reporter writing to w. verbose adds a line per run.
func NewConsole(w io.Writer, verbose bool) *Console { return &Console{w: w, verbose: verbose} }

func (c *Console) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.w, format, args...)
}

// RunStarted prints the header line.
func (c *Console) RunStarted(info RunInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.printf("tamesu run: %s, output in %s\n", plural(info.Tests, "test"), info.OutDir)
}

// TestStarted prints a START line in verbose mode.
func (c *Console) TestStarted(t *TestOutcome) {
	if !c.verbose {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.printf("START  %s / %s (%s, %s)\n", t.Suite, t.Name, t.Target, plural(t.Repeat, "run"))
}

// RunFinished prints one line per repeat in verbose mode.
func (c *Console) RunFinished(t *TestOutcome, r *RunOutcome) {
	if !c.verbose {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := "pass"
	switch {
	case r.ErrorKind != "":
		state = r.ErrorKind + ": " + r.Error
	case !r.Passed:
		state = "fail"
	}
	c.printf("  run %d/%d  %s  %s\n", r.Index, t.Repeat, fmtDuration(r.Duration), state)
}

// TestFinished prints the verdict and, on failure, the reasons and last turns.
func (c *Console) TestFinished(t *TestOutcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	label := "PASS "
	switch {
	case t.Errored():
		label = "ERROR"
	case !t.Passed:
		label = "FAIL "
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
	c.printf("%s  %s / %s  %s%s\n", label, t.Suite, t.Name, fmtDuration(dur), runs)
	if t.Passed {
		return
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
			c.printf("%s %s: %s\n", prefix, r.ErrorKind, r.Error)
		}
		for _, a := range r.FailedAssertions() {
			c.printf("%s x %s: %s\n", prefix, a.Name, a.Reason)
		}
		if n := len(r.Transcript); n > 0 {
			c.printf("%s last turns:\n", prefix)
			start := max(n-3, 0)
			for _, turn := range r.Transcript[start:] {
				c.printf("%s   %-6s %s\n", prefix, turn.Role+":", oneLine(turn.Text, 160))
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
	line := fmt.Sprintf("\n%d passed, %d failed, %d errors in %s", passed, failed, errored, fmtDuration(s.Duration()))
	if s.Usage.Calls > 0 {
		line += fmt.Sprintf(" · %d model calls, %d tokens, $%.4f", s.Usage.Calls, s.Usage.Total(), s.Usage.CostUSD)
	}
	if s.BudgetExceeded {
		line += " · BUDGET EXCEEDED: the run was stopped"
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
