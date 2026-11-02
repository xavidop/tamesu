// Package runner executes selected tests: it schedules repeats in parallel,
// opens a session per run, drives the persona loop, collects metrics,
// evaluates assertions, writes the run directory (design.md §9) and feeds
// the reporters. Retries, timeouts and the cost budget live here.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/internal/secrets"
	"github.com/xavidop/tamesu/report"
	"github.com/xavidop/tamesu/target"
)

// TargetSpec is what a TargetFactory gets to build a target for one test.
type TargetSpec struct {
	Name    string
	Config  config.Target
	Suite   *config.Suite
	Project config.Project
	// Mode is the effective voice mode (text or audio) for livekit, sip and realtime.
	Mode string
	// Headed shows the browser window (browser targets).
	Headed bool
}

// TargetFactory builds the target for a spec. It returns an error when the
// type is not available in this build.
type TargetFactory func(ctx context.Context, spec TargetSpec) (target.Target, error)

// Selection picks tests by name, tags and target.
type Selection struct {
	Filter *regexp.Regexp
	Tags   []string
	Target string
}

// Options configure a run.
type Options struct {
	Project config.Project
	Suites  []*config.Suite
	Select  Selection
	// OutDir is the run directory; it is created.
	OutDir string
	// Parallel caps concurrent runs; 0 means GOMAXPROCS.
	Parallel int
	// Limits cap concurrent runs per target type on top of Parallel
	// (livekit.max_concurrent_rooms, sip.max_concurrent_calls); 0 means no
	// extra cap.
	Limits map[string]int
	// Repeat overrides every test's repeat count when > 0.
	Repeat int
	// Load > 0 is load mode: every selected test runs with Load concurrent
	// virtual users, each looping runs for LoadDuration (one run each when
	// zero); Parallel defaults to Load times the number of tests. The
	// summary gets a load profile per test.
	Load         int
	LoadDuration time.Duration
	// Mode overrides every voice test's mode when set.
	Mode string
	// MaxCostUSD overrides the project budget when > 0.
	MaxCostUSD float64
	// Headed shows the browser window for browser tests (--headed).
	Headed bool
	// Replay runs browser goals from the action cache only (no agent):
	// a miss or a failed replay fails the step. UpdateCache ignores the
	// cache and refreshes it. CacheDir holds the plans (default .tamesu/cache).
	Replay      bool
	UpdateCache bool
	CacheDir    string
	Targets     TargetFactory
	// Provider is the AI layer; nil fails conversational tests with an
	// infrastructure error.
	Provider model.Provider
	Secrets  *secrets.Store
	// Vars are --var overrides on top of each suite's vars.
	Vars     map[string]string
	Env      config.EnvLookup
	Reporter report.Reporter
	Logger   *slog.Logger
	Now      func() time.Time
	Version  string
}

// ErrNoTests is returned when the selection matches nothing.
var ErrNoTests = errors.New("no tests selected")

// ErrBudgetExceeded cancels the run when tamesu's own model spend passes the budget.
var ErrBudgetExceeded = errors.New("cost budget exceeded")

type item struct {
	suite     *config.Suite
	test      config.Test
	target    config.Target
	outcome   *report.TestOutcome
	repeat    int
	threshold float64
	retries   int
	timeout   time.Duration
	mode      string
	seed      int64
	// maxGoalSteps caps the browser agent's actions per goal (0: default).
	maxGoalSteps int
	dir          string
}

type run struct {
	o          Options
	now        func() time.Time
	logger     *slog.Logger
	reporter   report.Reporter
	budgetUSD  float64
	budgetTok  int
	budgetHit  atomic.Bool
	usageStart model.Usage
}

// Run executes the selected tests and returns the summary. The error is only
// for setup problems (nothing selected, the output directory) or a reporter
// that failed to write; test failures are in the summary.
func Run(ctx context.Context, o Options) (*report.Summary, error) {
	r := &run{o: o, now: o.Now, logger: o.Logger, reporter: o.Reporter}
	if r.now == nil {
		r.now = time.Now
	}
	if r.logger == nil {
		r.logger = slog.New(slog.DiscardHandler)
	}
	if r.reporter == nil {
		r.reporter = report.Multi{}
	}
	parallel := o.Parallel
	if parallel <= 0 {
		parallel = runtime.GOMAXPROCS(0)
	}
	r.budgetUSD = o.MaxCostUSD
	if r.budgetUSD <= 0 {
		r.budgetUSD = o.Project.Budgets.MaxCostUSD
	}
	r.budgetTok = o.Project.Budgets.MaxTokens
	if o.Provider != nil {
		r.usageStart = o.Provider.Usage()
	}

	items, err := r.collect()
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNoTests
	}
	if o.Load > 0 && o.Parallel <= 0 {
		parallel = o.Load * len(items)
	}
	if err := os.MkdirAll(filepath.Join(o.OutDir, "tests"), 0o755); err != nil {
		return nil, fmt.Errorf("create run directory: %w", err)
	}

	summary := &report.Summary{StartedAt: r.now(), OutDir: o.OutDir, Version: o.Version}
	for _, it := range items {
		summary.Tests = append(summary.Tests, it.outcome)
	}
	r.reporter.RunStarted(report.RunInfo{OutDir: o.OutDir, Tests: len(items), StartedAt: summary.StartedAt})

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	sem := make(chan struct{}, parallel)
	typeSem := map[string]chan struct{}{}
	for typ, n := range o.Limits {
		if n > 0 {
			typeSem[typ] = make(chan struct{}, n)
		}
	}
	var wg sync.WaitGroup
	for _, it := range items {
		wg.Add(1)
		go func(it *item) {
			defer wg.Done()
			it.outcome.StartedAt = r.now()
			r.reporter.TestStarted(it.outcome)
			if o.Load > 0 {
				r.runLoad(runCtx, cancel, it, sem, typeSem[it.target.Type])
				return
			}
			runs := make([]*report.RunOutcome, it.repeat)
			var twg sync.WaitGroup
			for i := range runs {
				twg.Add(1)
				go func(i int) {
					defer twg.Done()
					select {
					case sem <- struct{}{}:
					case <-runCtx.Done():
						runs[i] = r.skipped(runCtx, it, i)
						return
					}
					defer func() { <-sem }()
					if ts := typeSem[it.target.Type]; ts != nil {
						select {
						case ts <- struct{}{}:
						case <-runCtx.Done():
							runs[i] = r.skipped(runCtx, it, i)
							return
						}
						defer func() { <-ts }()
					}
					runs[i] = r.runWithRetries(runCtx, it, i)
					r.reporter.RunFinished(it.outcome, runs[i])
					r.checkBudget(cancel)
				}(i)
			}
			twg.Wait()
			it.outcome.Runs = runs
			it.outcome.Finalize(r.now())
			r.reporter.TestFinished(it.outcome)
		}(it)
	}
	wg.Wait()

	summary.FinishedAt = r.now()
	summary.BudgetExceeded = r.budgetHit.Load()
	if o.Provider != nil {
		summary.Usage = usageDelta(r.usageStart, o.Provider.Usage())
	}
	if err := r.reporter.Finish(ctx, summary); err != nil {
		return summary, fmt.Errorf("reporter: %w", err)
	}
	return summary, nil
}

// runLoad is load mode for one test: Load virtual users each loop runs until
// LoadDuration elapses (once when it is zero), under the same semaphores as
// a normal run, so target caps still hold.
func (r *run) runLoad(ctx context.Context, cancel context.CancelCauseFunc, it *item, sem, typeSem chan struct{}) {
	var (
		mu    sync.Mutex
		runs  []*report.RunOutcome
		next  atomic.Int32
		uwg   sync.WaitGroup
		until time.Time
	)
	if r.o.LoadDuration > 0 {
		until = r.now().Add(r.o.LoadDuration)
	}
	for u := 0; u < r.o.Load; u++ {
		uwg.Add(1)
		go func() {
			defer uwg.Done()
			for {
				idx := int(next.Add(1)) - 1
				var out *report.RunOutcome
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					out = r.skipped(ctx, it, idx)
				}
				if out == nil {
					if typeSem != nil {
						select {
						case typeSem <- struct{}{}:
						case <-ctx.Done():
							<-sem
							out = r.skipped(ctx, it, idx)
						}
					}
					if out == nil {
						out = r.runWithRetries(ctx, it, idx)
						if typeSem != nil {
							<-typeSem
						}
						<-sem
					}
				}
				mu.Lock()
				runs = append(runs, out)
				mu.Unlock()
				r.reporter.RunFinished(it.outcome, out)
				r.checkBudget(cancel)
				if ctx.Err() != nil || until.IsZero() || !r.now().Before(until) {
					return
				}
			}
		}()
	}
	uwg.Wait()
	sort.Slice(runs, func(i, j int) bool { return runs[i].Index < runs[j].Index })
	it.outcome.Repeat = r.o.Load
	it.outcome.Runs = runs
	it.outcome.Finalize(r.now())
	it.outcome.Load = report.ComputeLoad(it.outcome, r.o.Load)
	r.reporter.TestFinished(it.outcome)
}

// collect resolves the selected tests of every suite into items with their
// effective settings and unique directories.
func (r *run) collect() ([]*item, error) {
	var items []*item
	slugs := map[string]int{}
	for _, s := range r.o.Suites {
		defaults := s.EffectiveDefaults(r.o.Project)
		for _, t := range s.SelectTests(r.o.Select.Filter, r.o.Select.Tags, r.o.Select.Target) {
			tgt, ok := s.TargetOf(t)
			if !ok {
				return nil, fmt.Errorf("%s: test %q: target %q is not declared", s.Path, t.Name, t.Target)
			}
			it := &item{suite: s, test: t, target: tgt}
			it.repeat = defaults.Repeat
			if t.Repeat != nil {
				it.repeat = *t.Repeat
			}
			if r.o.Repeat > 0 {
				it.repeat = r.o.Repeat
			}
			if it.repeat < 1 {
				it.repeat = 1
			}
			it.threshold = defaults.PassThreshold
			if t.PassThreshold != nil {
				it.threshold = *t.PassThreshold
			}
			if it.threshold <= 0 {
				it.threshold = 1
			}
			it.retries = defaults.Retries
			it.maxGoalSteps = defaults.MaxGoalSteps
			if t.Retries != nil {
				it.retries = *t.Retries
			}
			it.timeout = defaults.Timeout.Std()
			if t.Timeout != nil {
				it.timeout = t.Timeout.Std()
			}
			if it.timeout <= 0 {
				it.timeout = 3 * time.Minute
			}
			it.mode = t.Mode
			if it.mode == "" {
				it.mode = tgt.Mode
			}
			if r.o.Mode != "" && (tgt.Type == config.TargetLiveKit || tgt.Type == config.TargetSIP || tgt.Type == config.TargetRealtime) {
				it.mode = r.o.Mode
			}
			if tgt.Type == config.TargetSIP {
				it.mode = "audio"
			}
			if t.Seed != nil {
				it.seed = *t.Seed
			}
			slug := slugify(t.Name)
			slugs[slug]++
			if n := slugs[slug]; n > 1 {
				slug = fmt.Sprintf("%s-%d", slug, n)
			}
			it.dir = filepath.Join(r.o.OutDir, "tests", slug)
			it.outcome = &report.TestOutcome{
				Suite: s.Suite, File: s.Path, Name: t.Name, Target: t.Target, Type: tgt.Type, Tags: s.TagsOf(t),
				Repeat: it.repeat, PassThreshold: it.threshold, Dir: it.dir,
			}
			items = append(items, it)
		}
	}
	return items, nil
}

// checkBudget cancels the run once tamesu's own spend passes the budget.
func (r *run) checkBudget(cancel context.CancelCauseFunc) {
	if r.o.Provider == nil {
		return
	}
	u := usageDelta(r.usageStart, r.o.Provider.Usage())
	if (r.budgetUSD > 0 && u.CostUSD > r.budgetUSD) || (r.budgetTok > 0 && u.Total() > r.budgetTok) {
		if r.budgetHit.CompareAndSwap(false, true) {
			r.logger.Warn("budget exceeded, stopping the run", "cost_usd", u.CostUSD, "tokens", u.Total())
			cancel(ErrBudgetExceeded)
		}
	}
}

func usageDelta(start, now model.Usage) model.Usage {
	return model.Usage{
		Calls:        now.Calls - start.Calls,
		InputTokens:  now.InputTokens - start.InputTokens,
		OutputTokens: now.OutputTokens - start.OutputTokens,
		CostUSD:      now.CostUSD - start.CostUSD,
	}
}

// skipped is the outcome of a run that never started because the run was
// cancelled (budget or interrupt).
func (r *run) skipped(ctx context.Context, it *item, idx int) *report.RunOutcome {
	out := &report.RunOutcome{Index: idx + 1, Attempt: 1, Attempts: 1, StartedAt: r.now(), Metrics: map[string]float64{}, Dir: filepath.Join(it.dir, fmt.Sprintf("run-%d", idx+1))}
	out.ErrorKind, out.Error = classifyCancel(ctx)
	return out
}

func classifyCancel(ctx context.Context) (kind, msg string) {
	if errors.Is(context.Cause(ctx), ErrBudgetExceeded) {
		return report.KindBudget, "the run was stopped: " + ErrBudgetExceeded.Error()
	}
	return report.KindCancelled, "the run was cancelled"
}

func slugify(name string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	if s == "" {
		s = "test"
	}
	return s
}
