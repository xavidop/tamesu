package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/calibrate"
	"github.com/xavidop/tamesu/internal/replay"
	"github.com/xavidop/tamesu/internal/runner"
)

// DefaultMinAgreement is the agreement a calibration must reach to exit 0.
const DefaultMinAgreement = 0.8

func (a *app) newJudgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "judge",
		Short: "Work on the judge model: calibrate it against labelled transcripts",
		Long: "Judge groups the commands about the judge model. `tamesu judge calibrate` runs\n" +
			"the judge over transcripts a person has already marked pass or fail and reports\n" +
			"how often they agree, per rubric.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newJudgeCalibrateCmd())
	return cmd
}

func (a *app) newJudgeCalibrateCmd() *cobra.Command {
	var minAgreement float64
	var parallel int
	var out string
	var replayOnly, updateCache bool
	cmd := &cobra.Command{
		Use:   "calibrate <labels.yaml>",
		Short: "Run the judge over labelled transcripts and report the agreement per rubric",
		Long: "Calibrate reads labels (a YAML or JSON list of {name, rubric, scale, min, expected:\n" +
			"pass|fail, transcript: [{role, text}], tool_calls}), judges each transcript with\n" +
			"models.judge the way a judge assertion would, and prints the agreement per rubric,\n" +
			"the false passes and false fails, and every disagreement with the judge's reason.\n" +
			"Verdicts go through the llm cache like a run. Exit 1 when the agreement is under\n" +
			"--min-agreement, 2 on a usage or config error, 3 when the judge model failed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if minAgreement < 0 || minAgreement > 1 {
				return usageError(errors.New("--min-agreement is a fraction between 0 and 1"))
			}
			if replayOnly && updateCache {
				return usageError(errors.New("--replay and --update-cache exclude each other"))
			}
			labels, err := calibrate.Load(args[0])
			if err != nil {
				return configError(fmt.Errorf("labels %s: %w", args[0], err))
			}
			p, perrs, err := a.loadProject(cmd, true)
			if err != nil {
				return err
			}
			if perrs.HasErrors() {
				return configError(perrs)
			}
			provider, err := a.modelProvider(ctx, p)
			if err != nil {
				return err
			}
			defer func() { _ = provider.Close(context.WithoutCancel(ctx)) }()
			judge := provider
			var cache *replay.Provider
			if p.Cache.LLMCacheOn() {
				mode := replay.ModeAuto
				switch {
				case replayOnly:
					mode = replay.ModeReplay
				case updateCache:
					mode = replay.ModeUpdate
				}
				dir := p.Cache.Dir
				if dir == "" {
					dir = runner.DefaultCacheDir
				}
				cache = replay.New(provider, dir, mode)
				judge = cache
			}
			rep := calibrate.Run(ctx, judge, labels, parallel)
			rep.Print(cmd.OutOrStdout(), minAgreement)
			if cache != nil {
				s := cache.Stats()
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "llm cache: %d hits, %d misses\n", s.Hits, s.Misses)
			}
			if out != "" {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return infraError(err)
				}
				if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
					return infraError(err)
				}
				if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
					return infraError(err)
				}
			}
			if rep.Errors == rep.Labels {
				return infraError(fmt.Errorf("the judge failed on every label: %s", rep.Results[0].Error))
			}
			if rep.Agreement < minAgreement {
				return silentExit(ExitFailed)
			}
			return nil
		},
	}
	cmd.Flags().Float64Var(&minAgreement, "min-agreement", DefaultMinAgreement, "the agreement the judge must reach, as a fraction, else exit 1")
	cmd.Flags().IntVar(&parallel, "parallel", 4, "labels judged at a time")
	cmd.Flags().StringVar(&out, "out", "", "write the report as JSON here")
	cmd.Flags().BoolVar(&replayOnly, "replay", false, "answer only from the llm cache; a label not in the cache still calls the judge")
	cmd.Flags().BoolVar(&updateCache, "update-cache", false, "call the judge on every label and rewrite the cache")
	return cmd
}
