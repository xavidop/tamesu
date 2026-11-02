package report

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MarkdownFile is the summary the markdown and github reporters write.
const MarkdownFile = "summary.md"

// Markdown writes summary.md into the run directory: a table of the tests
// with the failing assertions' reasons, ready for a pull request comment
// (gh pr comment --body-file).
type Markdown struct{ dir string }

// NewMarkdown returns the markdown reporter for the run directory.
func NewMarkdown(dir string) *Markdown { return &Markdown{dir: dir} }

func (*Markdown) RunStarted(RunInfo)                    {}
func (*Markdown) TestStarted(*TestOutcome)              {}
func (*Markdown) RunFinished(*TestOutcome, *RunOutcome) {}
func (*Markdown) TestFinished(*TestOutcome)             {}

// Finish writes the file.
func (m *Markdown) Finish(_ context.Context, s *Summary) error {
	return os.WriteFile(filepath.Join(m.dir, MarkdownFile), []byte(RenderMarkdown(s)), 0o644)
}

// RenderMarkdown is the summary as GitHub-flavoured Markdown.
func RenderMarkdown(s *Summary) string {
	var b strings.Builder
	passed, failed, errored := s.Counts()
	icon := "✅"
	if failed > 0 || errored > 0 {
		icon = "❌"
	}
	fmt.Fprintf(&b, "## %s tamesu: %d passed, %d failed, %d errors\n\n", icon, passed, failed, errored)
	fmt.Fprintf(&b, "%s", fmtDuration(s.Duration()))
	if s.Usage.Calls > 0 {
		fmt.Fprintf(&b, " · %d model calls, %d tokens, $%.4f", s.Usage.Calls, s.Usage.Total(), s.Usage.CostUSD)
	}
	if s.BudgetExceeded {
		b.WriteString(" · **budget exceeded, the run was stopped**")
	}
	b.WriteString("\n\n")
	if c := s.Comparison; c != nil {
		verdict := "no regression"
		if c.Regressed {
			verdict = "**regressed**"
		}
		fmt.Fprintf(&b, "Against the baseline%s: %s · %d compared, %d new failures, %d fixed, %d added, %d removed\n\n", optional(" of ", c.BaselineAt), verdict, len(c.Tests), len(c.NewFailures), len(c.Fixed), len(c.Added), len(c.Removed))
	}
	b.WriteString("| | Test | Target | Runs | Time |\n|---|---|---|---|---|\n")
	for _, t := range s.Tests {
		status := "✅"
		switch {
		case t.Errored():
			status = "💥"
		case !t.Passed:
			status = "❌"
		}
		runs := fmt.Sprintf("%d", len(t.Runs))
		if t.Repeat > 1 {
			runs = fmt.Sprintf("%.0f%% of %d", t.PassRate*100, len(t.Runs))
		}
		fmt.Fprintf(&b, "| %s | %s | %s · %s (%s) | %s | %s |\n", status, mdEscape(t.Name), mdEscape(t.Suite), mdEscape(t.Target), t.Type, runs, fmtDuration(t.FinishedAt.Sub(t.StartedAt)))
	}
	details := failureDetails(s)
	if len(details) > 0 {
		b.WriteString("\n<details><summary>Failures</summary>\n\n")
		for _, d := range details {
			fmt.Fprintf(&b, "- **%s**: %s\n", mdEscape(d.test), mdEscape(d.reason))
		}
		b.WriteString("\n</details>\n")
	}
	return b.String()
}

type failure struct{ test, reason string }

// failureDetails lists each failed or errored test with its first reason.
func failureDetails(s *Summary) []failure {
	var out []failure
	for _, t := range s.Tests {
		if t.Passed {
			continue
		}
		reasons := map[string]bool{}
		for _, r := range t.Runs {
			if r == nil {
				continue
			}
			if r.Error != "" {
				reasons[r.ErrorKind+": "+r.Error] = true
				continue
			}
			for _, a := range r.FailedAssertions() {
				reasons[a.Name+": "+a.Reason] = true
			}
		}
		keys := make([]string, 0, len(reasons))
		for k := range reasons {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			keys = []string{"failed"}
		}
		out = append(out, failure{test: t.Suite + " / " + t.Name, reason: strings.Join(keys, "; ")})
	}
	return out
}

func optional(prefix, v string) string {
	if v == "" {
		return ""
	}
	return prefix + v
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}
