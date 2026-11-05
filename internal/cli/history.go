package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/report"
)

// DefaultHistoryRuns is how many runs the history looks back by default.
const DefaultHistoryRuns = 20

func (a *app) newHistoryCmd() *cobra.Command {
	var runsDir string
	var last int
	var asJSON, flakyOnly bool
	var sel selectFlags
	cmd := &cobra.Command{
		Use:   "history [--runs-dir .tamesu/runs] [--last N]",
		Short: "Each test's results and latency over the last runs",
		Long: "History reads the summaries under the runs directory, oldest first, and prints\n" +
			"one line per test: how many runs it was in, its pass rate, the verdict of every\n" +
			"run as a dot (✓ pass, ✗ fail, ! error, ~ quarantined, · skipped), the p95 latency\n" +
			"from the first to the last run, and whether it is flaky (its verdict changed two\n" +
			"or more times). --flaky keeps only those. The same view is in report.html when a\n" +
			"run lives under .tamesu/runs.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter, err := sel.compile()
			if err != nil {
				return err
			}
			h, err := report.LoadHistory(runsDir, last, "")
			if err != nil {
				return configError(fmt.Errorf("runs directory %s: %w", runsDir, err))
			}
			var tests []report.TestHistory
			for _, t := range h.Tests {
				if filter != nil && !filter.MatchString(t.Name) {
					continue
				}
				if flakyOnly && !t.Flaky() {
					continue
				}
				tests = append(tests, t)
			}
			out := cmd.OutOrStdout()
			if asJSON {
				if tests == nil {
					tests = []report.TestHistory{}
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Runs  []report.HistoryRun  `json:"runs"`
					Tests []report.TestHistory `json:"tests"`
				}{h.Runs, tests})
			}
			if len(h.Runs) == 0 {
				_, _ = fmt.Fprintf(out, "no runs with a summary.json under %s\n", runsDir)
				return nil
			}
			first := h.Runs[0].StartedAt
			lastRun := h.Runs[len(h.Runs)-1].StartedAt
			st := a.style(out)
			_, _ = fmt.Fprintf(out, "%s %s to %s\n\n", st.Bold(plural(len(h.Runs), "run")+","), first.Local().Format("2006-01-02 15:04"), lastRun.Local().Format("2006-01-02 15:04"))
			tw := table(out, st, "TEST", "RUNS", "PASS RATE", "RESULTS", "P95", "NOTE")
			for _, t := range tests {
				p95 := ""
				if t.P95First > 0 && t.P95Last > 0 {
					p95 = fmt.Sprintf("%.0f → %.0f ms", t.P95First, t.P95Last)
				}
				var notes []string
				if t.Flaky() {
					notes = append(notes, fmt.Sprintf("flaky (%d flips)", t.Flips))
				}
				if t.Streak > 1 && t.LatestStatus() != "pass" {
					notes = append(notes, fmt.Sprintf("%s %d× in a row", t.LatestStatus(), t.Streak))
				}
				rate := fmt.Sprintf("%.0f%%", t.PassRate*100)
				switch {
				case t.PassRate == 0:
					rate = st.Fail(rate)
				case t.PassRate < 1:
					rate = st.Warn(rate)
				}
				tw.Row(t.Suite+" / "+t.Name, fmt.Sprint(t.Runs), rate, t.Dots(), p95, st.Warn(strings.Join(notes, ", ")))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&runsDir, "runs-dir", filepath.Join(".tamesu", "runs"), "the directory holding the run directories")
	cmd.Flags().IntVar(&last, "last", DefaultHistoryRuns, "how many of the latest runs to read (0 for all)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON instead of a table")
	cmd.Flags().BoolVar(&flakyOnly, "flaky", false, "only tests whose verdict changed two or more times")
	cmd.Flags().StringVar(&sel.filter, "filter", "", "only tests whose name matches this regular expression")
	return cmd
}
