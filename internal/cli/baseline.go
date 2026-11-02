package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/report"
)

// thresholdsOf maps the project's baseline block onto report thresholds.
func thresholdsOf(b config.BaselineConfig) report.Thresholds {
	t := report.DefaultThresholds
	if b.MaxLatencyIncreasePct != nil {
		t.MaxLatencyIncreasePct = *b.MaxLatencyIncreasePct
	}
	if b.MaxCostIncreasePct != nil {
		t.MaxCostIncreasePct = *b.MaxCostIncreasePct
	}
	if b.FailOnNewFailures != nil {
		t.FailOnNewFailures = *b.FailOnNewFailures
	}
	return t
}

// compareWithBaseline loads the baseline summary from dir, compares, writes
// comparison.json into the run directory and prints the result. ok is
// false when the baseline cannot be read.
func compareWithBaseline(w io.Writer, baselineDir, runDir string, s *report.Summary, t report.Thresholds) (*report.Comparison, error) {
	base, err := report.LoadSummary(baselineDir)
	if err != nil {
		return nil, fmt.Errorf("baseline %s: %w (save one with tamesu baseline save <run-dir>)", baselineDir, err)
	}
	c := report.Compare(base, s, t)
	if c.BaselineDir == "" || c.BaselineDir == base.OutDir {
		c.BaselineDir = baselineDir
	}
	s.Comparison = c
	if runDir != "" {
		if err := report.WriteComparison(runDir, c); err != nil {
			return c, fmt.Errorf("write %s: %w", report.ComparisonFile, err)
		}
	}
	report.PrintComparison(w, c)
	return c, nil
}

func (a *app) newBaselineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "baseline",
		Short: "Keep a run as the baseline later runs are compared with",
		Long: "Baseline manages the run that tamesu run --baseline and tamesu compare measure\n" +
			"against: `tamesu baseline save <run-dir>` copies that run's summary.json into the\n" +
			"baseline directory (baseline.dir in tamesu.yaml, default .tamesu/baseline).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newBaselineSaveCmd())
	return cmd
}

func (a *app) newBaselineSaveCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "save <run-dir>",
		Short: "Copy a run's summary.json into the baseline directory",
		Long: "Save reads summary.json from a run directory and writes it into the baseline\n" +
			"directory (--dir, else baseline.dir in tamesu.yaml, else .tamesu/baseline), so later\n" +
			"runs can be compared with it (tamesu run --baseline, tamesu compare).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := report.LoadSummary(args[0])
			if err != nil {
				return configError(err)
			}
			if dir == "" {
				p, _, err := a.loadProject(cmd, false)
				if err == nil {
					dir = p.Baseline.EffectiveDir()
				} else {
					dir = config.DefaultBaselineDir
				}
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return infraError(err)
			}
			b, err := os.ReadFile(filepath.Join(args[0], "summary.json"))
			if err != nil {
				return configError(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "summary.json"), b, 0o644); err != nil {
				return infraError(err)
			}
			passed, failed, errored := s.Counts()
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "baseline saved to %s: %d tests (%d passed, %d failed, %d errors)\n", filepath.Join(dir, "summary.json"), len(s.Tests), passed, failed, errored)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "the baseline directory (default baseline.dir in tamesu.yaml, else .tamesu/baseline)")
	return cmd
}

func (a *app) newCompareCmd() *cobra.Command {
	var maxLatency, maxCost float64
	var newFailures bool
	cmd := &cobra.Command{
		Use:   "compare <baseline-dir> <run-dir>",
		Short: "Compare a run with a baseline and exit 1 on a regression",
		Long: "Compare reads the summary.json of a baseline and of a run, matches the tests by\n" +
			"suite and name, and reports new failures, fixed tests and the tests whose p95\n" +
			"latency or cost grew beyond the limits (--max-latency-increase-pct,\n" +
			"--max-cost-increase-pct, --fail-on-new-failures; defaults from baseline: in\n" +
			"tamesu.yaml). It writes comparison.json into the run directory and exits 1 when\n" +
			"the run regressed, 0 otherwise.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t := report.DefaultThresholds
			if p, _, err := a.loadProject(cmd, false); err == nil {
				t = thresholdsOf(p.Baseline)
			}
			if cmd.Flags().Changed("max-latency-increase-pct") {
				t.MaxLatencyIncreasePct = maxLatency
			}
			if cmd.Flags().Changed("max-cost-increase-pct") {
				t.MaxCostIncreasePct = maxCost
			}
			if cmd.Flags().Changed("fail-on-new-failures") {
				t.FailOnNewFailures = newFailures
			}
			s, err := report.LoadSummary(args[1])
			if err != nil {
				return configError(err)
			}
			c, err := compareWithBaseline(cmd.OutOrStdout(), args[0], args[1], s, t)
			if err != nil {
				return configError(err)
			}
			if c.Regressed {
				return silentExit(ExitFailed)
			}
			return nil
		},
	}
	cmd.Flags().Float64Var(&maxLatency, "max-latency-increase-pct", report.DefaultThresholds.MaxLatencyIncreasePct, "how much a test's p95 latency metrics may grow (0 disables)")
	cmd.Flags().Float64Var(&maxCost, "max-cost-increase-pct", report.DefaultThresholds.MaxCostIncreasePct, "how much a test's llm_cost_usd may grow (0 disables)")
	cmd.Flags().BoolVar(&newFailures, "fail-on-new-failures", true, "a test that passed in the baseline and fails now is a regression")
	return cmd
}
