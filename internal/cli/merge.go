package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/report"
)

func (a *app) newMergeCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "merge <run-dir> [<run-dir>...] --out <dir>",
		Short: "Combine the run directories of shards or repeated runs into one run",
		Long: "Merge reads the summary.json of each run directory and writes one run directory\n" +
			"with every test: the shards of a suite side by side, or the same tests from\n" +
			"repeated runs as one test with more runs. The run directories are copied under\n" +
			"the output, so report.html keeps its links, and summary.json, junit.xml,\n" +
			"report.html and summary.md are written. Labels are the union of the inputs.\n" +
			"The merged directory works with tamesu compare and tamesu baseline save. Exit\n" +
			"codes are those of tamesu run, for the merged result.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				out = filepath.Join(".tamesu", "runs", a.deps.Now().Format("2006-01-02T15-04-05")+"-merged")
			}
			s, err := report.Merge(args, out)
			if err != nil {
				return configError(err)
			}
			passed, failed, errored := s.Counts()
			st := a.style(cmd.OutOrStdout())
			line := fmt.Sprintf("%s: %d tests from %d runs · %s", st.Info(out), len(s.Tests), len(args), counts(st, passed, failed, errored))
			if q := len(s.Quarantined()); q > 0 {
				line += fmt.Sprintf(", %d quarantined", q)
			}
			if n := len(s.Skipped()); n > 0 {
				line += fmt.Sprintf(", %d skipped", n)
			}
			if l := report.FormatLabels(s.Labels); l != "" {
				line += " · " + l
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
			if code := s.ExitCode(); code != ExitOK {
				return silentExit(code)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "the merged run directory (default .tamesu/runs/<timestamp>-merged)")
	return cmd
}
