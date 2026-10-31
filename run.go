package tamesu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/internal/secrets"
	"github.com/xavidop/tamesu/report"
	"github.com/xavidop/tamesu/target"
)

// Provider is the AI layer a test needs: the simulated user, the judge, the
// evaluators and the usage meter. *llm.Client implements it.
type Provider = model.Provider

// RunOption configures Run.
type RunOption func(*runOptions)

type runOptions struct {
	provider  Provider
	secrets   map[string]string
	mamori    secrets.Options
	vars      map[string]string
	outDir    string
	reporters []report.Reporter
	project   config.Project
}

// WithProvider sets the AI layer (an *llm.Client).
func WithProvider(p Provider) RunOption { return func(o *runOptions) { o.provider = p } }

// WithSecrets declares secrets as mamori refs, resolved once before the test
// runs and available as {{secrets.NAME}} to the target.
func WithSecrets(refs map[string]string) RunOption {
	return func(o *runOptions) {
		if o.secrets == nil {
			o.secrets = map[string]string{}
		}
		for k, v := range refs {
			o.secrets[k] = v
		}
	}
}

// WithMamoriOptions configures how secrets are resolved: ref vars,
// providers, the env file.
func WithMamoriOptions(opts secrets.Options) RunOption {
	return func(o *runOptions) { o.mamori = opts }
}

// WithVars sets {{vars.x}} values.
func WithVars(vars map[string]string) RunOption { return func(o *runOptions) { o.vars = vars } }

// WithOutDir sets the run directory (default $TAMESU_OUT, else the test's temp dir).
func WithOutDir(dir string) RunOption { return func(o *runOptions) { o.outDir = dir } }

// WithReporter adds a reporter (the console reporter on the test log is always on).
func WithReporter(r report.Reporter) RunOption {
	return func(o *runOptions) { o.reporters = append(o.reporters, r) }
}

// WithProject applies a project configuration (defaults, budgets); the default
// is config's DefaultProject.
func WithProject(p config.Project) RunOption { return func(o *runOptions) { o.project = p } }

// Run runs one test from Go. It makes a subtest per repeat (when t is a
// *testing.T), logs the transcript, fails the test when the pass threshold is
// not met and writes the run directory to TAMESU_OUT or the test's temp dir.
func Run(t testing.TB, test Test, opts ...RunOption) *report.TestOutcome {
	t.Helper()
	o := runOptions{project: config.DefaultProject()}
	for _, opt := range opts {
		opt(&o)
	}
	if test.Target == nil {
		t.Fatal("tamesu.Run: Test.Target is nil")
	}
	if test.Name == "" {
		test.Name = t.Name()
	}
	if o.outDir == "" {
		o.outDir = os.Getenv("TAMESU_OUT")
	}
	if o.outDir == "" {
		o.outDir = t.TempDir()
	}
	ctx := t.Context()

	var store *secrets.Store
	if len(o.secrets) > 0 {
		var err error
		store, err = secrets.Resolve(ctx, o.secrets, o.mamori)
		if err != nil {
			t.Fatalf("tamesu.Run: secrets: %v", err)
		}
	}

	// The Go assertions travel through the custom registry under names that
	// cannot clash with a suite's.
	var cfgAsserts []config.Assertion
	for i, a := range test.Assert {
		name := fmt.Sprintf("go:%s:%d", test.Name, i)
		a := a
		assert.Register(name, func() assert.Assertion { return a })
		cfgAsserts = append(cfgAsserts, config.Assertion{Custom: &name})
	}
	suite := &config.Suite{
		Suite:   "go",
		Path:    "go",
		Vars:    o.vars,
		Targets: map[string]config.Target{test.Target.Name(): {Type: "go"}},
		Tests: []config.Test{{
			Name:   test.Name,
			Target: test.Target.Name(),
			Persona: &config.Persona{Name: test.Persona.Name, Style: test.Persona.Style, Language: test.Persona.Language, Voice: test.Persona.Voice,
				Behaviors: &config.Behaviors{InterruptProbability: test.Persona.InterruptProbability, SilenceAfterTurnMs: test.Persona.SilenceAfterTurnMs[:], BackgroundNoise: test.Persona.BackgroundNoise, LineQuality: test.Persona.LineQuality}},
			Scenario: &config.Scenario{Goal: test.Scenario.Goal, MaxTurns: test.Scenario.MaxTurns, StopWhen: test.Scenario.StopWhen},
			Assert:   cfgAsserts,
		}},
	}
	tt := &suite.Tests[0]
	if test.Repeat > 0 {
		tt.Repeat = &test.Repeat
	}
	if test.PassThreshold > 0 {
		tt.PassThreshold = &test.PassThreshold
	}
	if test.Retries > 0 {
		tt.Retries = &test.Retries
	}
	if test.Timeout > 0 {
		d := config.Duration(test.Timeout)
		tt.Timeout = &d
	}
	if test.Seed != 0 {
		tt.Seed = &test.Seed
	}
	if test.Persona.SilenceAfterTurnMs == [2]int{} {
		tt.Persona.Behaviors.SilenceAfterTurnMs = nil
	}

	reporters := append(report.Multi{report.NewConsole(testLogWriter{t}, true), report.NewJSON(o.outDir), report.NewJUnit(o.outDir)}, o.reporters...)
	summary, err := runner.Run(ctx, runner.Options{
		Project:  o.project,
		Suites:   []*config.Suite{suite},
		OutDir:   o.outDir,
		Parallel: 1,
		Targets:  func(context.Context, runner.TargetSpec) (target.Target, error) { return test.Target, nil },
		Provider: o.provider,
		Secrets:  store,
		Vars:     o.vars,
		Env:      func(name string) (string, bool) { return os.LookupEnv(name) },
		Reporter: reporters,
		Now:      time.Now,
	})
	if err != nil {
		t.Fatalf("tamesu.Run: %v", err)
	}
	outcome := summary.Tests[0]
	report1 := func(t testing.TB, r *report.RunOutcome) {
		for _, turn := range r.Transcript {
			t.Logf("%s: %s", turn.Role, turn.Text)
		}
		if r.Error != "" {
			t.Errorf("%s: %s", r.ErrorKind, r.Error)
		}
		for _, a := range r.FailedAssertions() {
			t.Errorf("%s: %s", a.Name, a.Reason)
		}
	}
	for _, r := range outcome.Runs {
		if tt, ok := t.(*testing.T); ok {
			tt.Run(fmt.Sprintf("run-%d", r.Index), func(t *testing.T) { report1(t, r) })
		} else {
			report1(t, r)
		}
	}
	if !outcome.Passed {
		t.Errorf("tamesu: %q: %d of %d runs passed (threshold %.0f%%); run directory: %s", test.Name, int(outcome.PassRate*float64(len(outcome.Runs))+0.5), len(outcome.Runs), outcome.PassThreshold*100, filepath.Clean(o.outDir))
	}
	return outcome
}

// testLogWriter sends the console reporter's lines to t.Log.
type testLogWriter struct{ t testing.TB }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
