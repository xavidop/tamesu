package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/internal/replay"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/internal/secrets"
	"github.com/xavidop/tamesu/report"
	"github.com/xavidop/tamesu/target"
)

// ProviderConfig is what cmd/tamesu needs to build the AI layer for a run.
type ProviderConfig struct {
	Project config.Project
	// Models are the effective models (project, overridden by the suites).
	Models config.Models
	// Secrets holds the resolved secrets; model credentials are read from it
	// by conventional name or through models.credentials.
	Secrets *secrets.Store
	Logger  *slog.Logger
	// MaxCostUSD is the effective cost budget.
	MaxCostUSD float64
	MaxTokens  int
}

// Credentials returns the lookup the llm module uses for plugin credentials:
// models.credentials first ({googleai: "{{secrets.X}}"}), then the secret
// with the conventional name.
func (p ProviderConfig) Credentials(name string) (string, bool) {
	if p.Secrets == nil {
		return "", false
	}
	for plugin, tmpl := range p.Models.Credentials {
		if !strings.EqualFold(pluginForCredential(name), plugin) {
			continue
		}
		for _, secret := range config.SecretRefs(tmpl) {
			if v, ok := p.Secrets.Lookup(secret); ok {
				return v, true
			}
		}
	}
	return p.Secrets.Lookup(name)
}

// pluginForCredential maps a conventional credential name to its plugin.
func pluginForCredential(name string) string {
	switch name {
	case "GEMINI_API_KEY", "GOOGLE_API_KEY":
		return "googleai"
	case "ANTHROPIC_API_KEY":
		return "anthropic"
	case "OPENAI_API_KEY":
		return "openai"
	case "DEEPGRAM_API_KEY":
		return "deepgram"
	case "ELEVENLABS_API_KEY":
		return "elevenlabs"
	case "CARTESIA_API_KEY":
		return "cartesia"
	case "SONIOX_API_KEY":
		return "soniox"
	case "ASSEMBLYAI_API_KEY":
		return "assemblyai"
	case "AZURE_SPEECH_KEY":
		return "azure"
	case "RIME_API_KEY":
		return "rime"
	case "GOOGLE_CLOUD_API_KEY":
		return "google"
	case "SPEECHMATICS_API_KEY":
		return "speechmatics"
	case "GLADIA_API_KEY":
		return "gladia"
	}
	return ""
}

// TargetFactory builds a target of one type.
type TargetFactory func(ctx context.Context, spec runner.TargetSpec) (target.Target, error)

// supportedReporters are the reporters this build has.
var supportedReporters = []string{"console", "json", "junit", "html", "markdown", "github", "slack", "sarif"}

func (a *app) newRunCmd() *cobra.Command {
	var (
		sel          selectFlags
		parallel     int
		repeat       int
		mode         string
		headed       bool
		replayOnly   bool
		updateGolden bool
		baseline     bool
		baselineDir  string
		updateVisual bool
		labelFlags   []string
		failFast     bool
		updateCache  bool
		out          string
		reporters    []string
		maxCostUSD   float64
		load         int
		loadFor      time.Duration
		estimateOnly bool
		matrixFlags  []string
		envName      string
	)
	cmd := &cobra.Command{
		Use:   "run [paths...]",
		Short: "Run the suites",
		Long: "Run loads tamesu.yaml and the suites (the paths and globs given, or the project's\n" +
			"suites:), resolves the secrets the selected tests use through mamori, and runs every\n" +
			"test against its target, repeats in parallel. Each run writes a directory under\n" +
			"--out with transcript.json and result.json; the reporters write summary.json,\n" +
			"junit.xml and the console output.\n\n" +
			"Exit codes: 0 all passed, 1 a test failed, 2 config error, 3 infrastructure error,\n" +
			"4 the cost budget was exceeded.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			filter, err := sel.compile()
			if err != nil {
				return err
			}
			if mode != "" && mode != "text" && mode != "audio" {
				return usageError(fmt.Errorf("--mode must be text or audio"))
			}
			if load < 0 {
				return usageError(fmt.Errorf("--load must be 1 or more"))
			}
			if loadFor != 0 && load == 0 {
				return usageError(fmt.Errorf("--load-duration needs --load"))
			}
			if load > 0 && repeat > 0 {
				return usageError(fmt.Errorf("--load and --repeat exclude each other: load mode sets the runs"))
			}
			flagLabels, err := parseLabelFlags(labelFlags)
			if err != nil {
				return usageError(err)
			}
			shard, err := sel.shardOf()
			if err != nil {
				return usageError(err)
			}
			if shard.Active() {
				if _, ok := flagLabels["shard"]; !ok {
					flagLabels["shard"] = shard.String()
				}
			}
			if out == "" {
				out = filepath.Join(".tamesu", "runs", a.deps.Now().Format("2006-01-02T15-04-05"))
			}
			variants, err := parseMatrixFlags(matrixFlags)
			if err != nil {
				return usageError(err)
			}
			// runOnce runs the selection into outDir with a matrix variant's
			// values applied (none for a plain run) and returns the summary and
			// its exit code.
			runOnce := func(outDir string, variant matrixVariant) (*report.Summary, int, error) {
				out := outDir
				p, perrs, err := a.loadProject(cmd, true)
				if err != nil {
					return nil, 0, err
				}
				paths, err := a.suitePaths(args, p)
				if err != nil {
					return nil, 0, err
				}
				files, err := a.loadSuites(paths, p, true)
				if err != nil {
					return nil, 0, err
				}
				nErr, _ := printErrors(cmd.ErrOrStderr(), perrs.Only(), a.style(cmd.ErrOrStderr()))
				var suites []*config.Suite
				for _, f := range files {
					e, _ := printErrors(cmd.ErrOrStderr(), f.Errs.Only(), a.style(cmd.ErrOrStderr()))
					nErr += e
					if e == 0 && f.Suite != nil {
						suites = append(suites, f.Suite)
					}
				}
				if nErr > 0 {
					return nil, ExitConfig, nil
				}
				if err := p.ApplyEnvironment(envName); err != nil {
					return nil, 0, configError(err)
				}
				if err := applyMatrix(&p, suites, variant); err != nil {
					return nil, 0, usageError(err)
				}

				// Reporters.
				if len(reporters) == 0 {
					reporters = p.Reporters
				}
				if !slices.Contains(reporters, "json") {
					reporters = append(reporters, "json")
				}
				var reps report.Multi
				wantSlack := false
				for _, r := range reporters {
					switch r {
					case "console":
						reps = append(reps, report.NewConsole(cmd.OutOrStdout(), a.verbose).WithColor(a.style(cmd.OutOrStdout()).Enabled()))
					case "json":
						reps = append(reps, report.NewJSON(out))
					case "junit":
						reps = append(reps, report.NewJUnit(out))
					case "sarif":
						reps = append(reps, report.NewSARIF(out))
					case "html":
						html := report.NewHTML(out)
						if report.IsRunsDir(out) {
							html = html.WithHistory(filepath.Dir(out), DefaultHistoryRuns)
						}
						reps = append(reps, html)
					case "markdown":
						reps = append(reps, report.NewMarkdown(out))
					case "github":
						reps = append(reps, report.NewGitHub(out, cmd.OutOrStdout()))
					case "slack":
						wantSlack = true
					default:
						return nil, 0, usageError(fmt.Errorf("--reporter: unknown reporter %q (have %s)", r, strings.Join(supportedReporters, ", ")))
					}
				}

				// Selection and secrets: only what the selected tests use.
				selection := runner.Selection{Filter: filter, Tags: sel.tags, Target: sel.target, Shard: shard}
				refs := map[string]string{}
				refVars := map[string]string{}
				selected := 0
				models := p.Models
				for _, s := range suites {
					tests := s.SelectTests(filter, sel.tags, sel.target)
					selected += len(tests)
					if len(tests) == 0 {
						continue
					}
					for k, v := range secrets.Collect(p, s, tests) {
						refs[k] = v
					}
					for k, v := range s.Vars {
						refVars[k] = v
					}
					models = s.EffectiveModels(p)
				}
				if selected == 0 {
					return nil, 0, configError(fmt.Errorf("no tests match the selection in %s", plural(len(suites), "suite")))
				}
				if estimateOnly {
					// The runs next to this one: .tamesu/runs by default, the
					// parent of --out otherwise.
					h, _ := report.LoadHistory(filepath.Dir(out), DefaultHistoryRuns, "")
					seen := 0
					if h != nil {
						seen = len(h.Runs)
					}
					budget := maxCostUSD
					if budget <= 0 {
						budget = p.Budgets.MaxCostUSD
					}
					printEstimate(cmd.OutOrStdout(), estimateCost(p, suites, filter, sel.tags, sel.target, repeat, h), seen, budget, a.style(cmd.OutOrStdout()))
					return nil, ExitOK, nil
				}
				if wantSlack {
					declared := secrets.Declared(p, nil)
					for _, tmpl := range []string{p.Slack.Webhook, p.Slack.Link} {
						for _, name := range config.SecretRefs(tmpl) {
							if ref, ok := declared[name]; ok {
								refs[name] = ref
							}
						}
					}
				}
				flagVars, err := a.refVars(nil)
				if err != nil {
					return nil, 0, err
				}
				if envVars := p.EnvironmentVars(); len(envVars) > 0 {
					// The environment's vars win over the suites'; --var still wins.
					merged := make(map[string]string, len(envVars)+len(flagVars))
					for k, v := range envVars {
						merged[k] = v
					}
					for k, v := range flagVars {
						merged[k] = v
					}
					flagVars = merged
				}
				if err := checkAllowedNumbers(p, suites, filter, sel.tags, sel.target, flagVars, a.env()); err != nil {
					return nil, 0, configError(err)
				}
				for k, v := range flagVars {
					refVars[k] = v
				}
				store, err := secrets.Resolve(ctx, refs, secrets.Options{RefVars: refVars, EnvFile: a.envFile, Providers: a.deps.Providers})
				if err != nil {
					return nil, 0, err
				}
				for _, name := range store.Names() {
					if v, ok := store.Lookup(name); ok {
						a.redactor.Add(name, v)
					}
				}
				if wantSlack {
					if p.Slack.Webhook == "" {
						return nil, 0, configError(errors.New("the slack reporter needs slack.webhook in tamesu.yaml (a {{secrets.NAME}})"))
					}
					sc := config.Scope{Env: a.env(), Vars: refVars, Secrets: store.Lookup}
					hook, err := config.Render(p.Slack.Webhook, sc)
					if err != nil {
						return nil, 0, configError(fmt.Errorf("slack.webhook: %w", err))
					}
					sl := report.NewSlack(hook, p.Slack.OnlyFailures)
					if sl.Link, err = config.Render(p.Slack.Link, sc); err != nil {
						return nil, 0, configError(fmt.Errorf("slack.link: %w", err))
					}
					reps = append(reps, sl)
				}
				logger := a.logger()
				slog.SetDefault(logger)

				budget := maxCostUSD
				if budget <= 0 {
					budget = p.Budgets.MaxCostUSD
				}
				var provider model.Provider
				if a.deps.NewProvider != nil {
					provider, err = a.deps.NewProvider(ctx, ProviderConfig{Project: p, Models: models, Secrets: store, Logger: logger, MaxCostUSD: budget, MaxTokens: p.Budgets.MaxTokens})
					if err != nil {
						return nil, 0, infraError(fmt.Errorf("model provider: %w", err))
					}
					defer func() { _ = provider.Close(context.WithoutCancel(ctx)) }()
				}
				var llmCache func() report.CacheStats
				if provider != nil && p.Cache.LLMCacheOn() {
					mode := replay.ModeAuto
					switch {
					case replayOnly:
						mode = replay.ModeReplay
					case updateCache:
						mode = replay.ModeUpdate
					}
					cacheDir := p.Cache.Dir
					if cacheDir == "" {
						cacheDir = runner.DefaultCacheDir
					}
					rp := replay.New(provider, cacheDir, mode)
					provider = rp
					llmCache = func() report.CacheStats { s := rp.Stats(); return report.CacheStats{Hits: s.Hits, Misses: s.Misses} }
				}

				variantLabels := maps.Clone(flagLabels)
				maps.Copy(variantLabels, variant.Labels())
				labels, err := runLabels(p.Labels, variantLabels, a.env())
				if err != nil {
					return nil, 0, configError(err)
				}
				summary, err := runner.Run(ctx, runner.Options{
					Project: p, Suites: suites, Select: selection, OutDir: out, Parallel: parallel, Repeat: repeat, Mode: mode,
					Load: load, LoadDuration: loadFor, FailFast: failFast,
					MaxCostUSD: budget, Headed: headed, Replay: replayOnly, UpdateCache: updateCache, CacheDir: p.Cache.Dir, UpdateGolden: updateGolden, UpdateVisual: updateVisual, LLMCache: llmCache, Labels: labels,
					Targets: a.targetFactory(), RemoteBinding: a.deps.RemoteBinding, Provider: provider, Secrets: store, Vars: flagVars, Env: a.env(),
					Limits:   map[string]int{config.TargetLiveKit: p.LiveKit.MaxConcurrentRooms, config.TargetSIP: p.SIP.MaxConcurrentCalls},
					Reporter: reps, Logger: logger, Now: a.deps.Now, Version: a.info.Version,
				})
				if err != nil {
					if errors.Is(err, runner.ErrEmptyShard) {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "shard %s: no tests fall into this shard (%d selected); nothing to run\n", shard, selected)
						return nil, ExitOK, nil
					}
					if errors.Is(err, runner.ErrNoTests) {
						return nil, 0, configError(err)
					}
					return nil, 0, infraError(err)
				}
				code := summary.ExitCode()
				if cerr := report.WriteCoverage(out, report.BuildCoverage(summary, declaredTools(suites))); cerr != nil {
					logger.Warn("writing the coverage report failed", "error", cerr)
				}
				if baseline || baselineDir != "" {
					dir := baselineDir
					if dir == "" {
						dir = p.Baseline.EffectiveDir()
					}
					c, cerr := compareWithBaseline(cmd.OutOrStdout(), dir, out, summary, thresholdsOf(p.Baseline), a.style(cmd.OutOrStdout()))
					if cerr != nil {
						return nil, 0, configError(cerr)
					}
					// The summary now carries the comparison; rewrite the files
					// that show it.
					_ = report.NewJSON(out).Finish(ctx, summary)
					if slices.Contains(reporters, "html") {
						_ = report.WriteHTML(out, summary)
					}
					if c.Regressed && code == ExitOK {
						code = ExitFailed
					}
				}
				return summary, code, nil
			}
			if len(variants) == 0 {
				_, code, err := runOnce(out, nil)
				if err != nil {
					return err
				}
				if code != ExitOK {
					return silentExit(code)
				}
				return nil
			}
			return a.runMatrix(cmd, out, variants, runOnce)
		},
	}
	sel.add(cmd)
	cmd.Flags().BoolVar(&baseline, "baseline", false, "after the run, compare it with the baseline (baseline.dir in tamesu.yaml, default .tamesu/baseline) and exit 1 on a regression")
	cmd.Flags().StringVar(&baselineDir, "baseline-dir", "", "the baseline directory to compare with (implies --baseline)")
	cmd.Flags().IntVar(&parallel, "parallel", 0, "concurrent runs (default: the number of CPUs)")
	cmd.Flags().IntVar(&repeat, "repeat", 0, "override every test's repeat count")
	cmd.Flags().IntVar(&load, "load", 0, "load mode: run every selected test with N concurrent virtual users (--parallel defaults to N per test)")
	cmd.Flags().DurationVar(&loadFor, "load-duration", 0, "load mode: each virtual user keeps looping runs for this long (default: one run each)")
	cmd.Flags().StringVar(&mode, "mode", "", "voice tests: text or audio (default from the suite)")
	cmd.Flags().BoolVar(&headed, "headed", false, "browser tests: show the browser window instead of running headless")
	cmd.Flags().BoolVar(&replayOnly, "replay", false, "replay from the caches: browser goals from cached plans and simulator turns from cached answers (a miss fails); judge verdicts from the cache when present")
	cmd.Flags().BoolVar(&updateCache, "update-cache", false, "ignore the caches (action plans, simulator turns, judge verdicts), call the models and refresh them")
	cmd.MarkFlagsMutuallyExclusive("replay", "update-cache")
	cmd.Flags().BoolVar(&updateGolden, "update-golden", false, "golden assertions: rewrite every golden file from this run's transcripts")
	cmd.Flags().BoolVar(&updateVisual, "update-visual", false, "browser visual steps: rewrite the baseline images from this run")
	cmd.Flags().StringArrayVar(&labelFlags, "label", nil, "label the run, KEY=VALUE (repeatable; on top of labels: in tamesu.yaml): the agent's build, the environment")
	cmd.Flags().StringVar(&envName, "env", "", "run against a named environment from environments: in tamesu.yaml (its vars, secrets and an env label)")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "stop starting new runs after the first failed run; runs in flight finish, the rest are reported as skipped")
	cmd.Flags().StringVar(&out, "out", "", "run output directory (default .tamesu/runs/<timestamp>)")
	cmd.Flags().StringSliceVar(&reporters, "reporter", nil, "reporters: console, json, junit, html, markdown, github, slack (default from tamesu.yaml)")
	cmd.Flags().Float64Var(&maxCostUSD, "max-cost-usd", 0, "stop the run once tamesu's own model spend exceeds this (default budgets.max_cost_usd)")
	cmd.Flags().BoolVar(&estimateOnly, "estimate", false, "price the selected tests from the last runs' usage (median cost per run, times repeat) and exit without running")
	cmd.Flags().StringArrayVar(&matrixFlags, "matrix", nil, "run every combination of KEY=value1,value2 (repeatable): models.<job>, speech.tts.model, speech.stt.model, vars.<name> or targets.<name>.<model|system|system_file|url|mode|streaming>; each variant runs into its own directory under --out, then they are compared")
	return cmd
}

// targetFactory dispatches on the target type to the factories cmd/tamesu
// registered.
func (a *app) targetFactory() runner.TargetFactory {
	return func(ctx context.Context, spec runner.TargetSpec) (target.Target, error) {
		f, ok := a.deps.Targets[spec.Config.Type]
		if !ok {
			return nil, fmt.Errorf("target type %s is not available in this build", spec.Config.Type)
		}
		return f(ctx, spec)
	}
}

func infraError(err error) error { return &exitError{code: ExitInfra, err: err} }

// parseLabelFlags reads --label KEY=VALUE flags.
func parseLabelFlags(flags []string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range flags {
		k, v, ok := strings.Cut(f, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("--label %q: want KEY=VALUE", f)
		}
		out[k] = v
	}
	return out, nil
}

// runLabels merges the project's labels (${VAR} expanded) with the flags.
func runLabels(project, flags map[string]string, env config.EnvLookup) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range project {
		expanded, _, err := config.ExpandEnv(v, env, false)
		if err != nil {
			return nil, fmt.Errorf("labels.%s: %w", k, err)
		}
		out[k] = expanded
	}
	for k, v := range flags {
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
