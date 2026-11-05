package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/report"
)

func (a *app) newReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report <run-dir>",
		Short: "Rebuild report.html from a run directory",
		Long: "Report reads summary.json from a run directory (.tamesu/runs/<timestamp>, or\n" +
			"--out of a run) and writes report.html next to it: one self-contained page with\n" +
			"every test's assertions and reasons, transcript, metrics, events, screenshots\n" +
			"and audio, linking the run's artifacts. The html reporter writes the same file\n" +
			"during tamesu run.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			// A run under .tamesu/runs gets a history section from its siblings.
			s, err := report.LoadSummary(dir)
			if err != nil {
				return configError(err)
			}
			var hist *report.History
			if report.IsRunsDir(dir) {
				hist = report.HistoryFor(filepath.Dir(dir), DefaultHistoryRuns, dir, s)
			}
			if err := report.WriteHTMLWithHistory(dir, s, hist); err != nil {
				return infraError(fmt.Errorf("write report: %w", err))
			}
			passed, failed, errored := s.Counts()
			st := a.style(cmd.OutOrStdout())
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", st.Info(filepath.Join(dir, report.HTMLFile)), counts(st, passed, failed, errored))
			return nil
		},
	}
	return cmd
}
