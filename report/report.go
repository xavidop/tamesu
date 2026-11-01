// Package report holds the outcome of a run (tests, repeats, assertions,
// metrics, transcripts) and the reporters that render it: console, JSON,
// JUnit and the self-contained HTML report.
package report

import (
	"context"
	"time"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

// Error kinds of a run.
const (
	KindTimeout   = "timeout"   // the test exceeded its timeout (a failure)
	KindInfra     = "infra"     // the target, a model or a provider failed (exit 3)
	KindBudget    = "budget"    // the run was stopped by the cost budget (exit 4)
	KindCancelled = "cancelled" // the run was interrupted
)

// RunInfo describes the whole invocation, for Reporter.RunStarted.
type RunInfo struct {
	OutDir    string    `json:"out_dir"`
	Tests     int       `json:"tests"`
	StartedAt time.Time `json:"started_at"`
}

// TestOutcome is one test with all its repeats.
type TestOutcome struct {
	Suite         string        `json:"suite"`
	File          string        `json:"file"`
	Name          string        `json:"name"`
	Target        string        `json:"target"`
	Type          string        `json:"type"`
	Tags          []string      `json:"tags,omitempty"`
	Repeat        int           `json:"repeat"`
	PassThreshold float64       `json:"pass_threshold"`
	Runs          []*RunOutcome `json:"runs"`
	Passed        bool          `json:"passed"`
	PassRate      float64       `json:"pass_rate"`
	StartedAt     time.Time     `json:"started_at"`
	FinishedAt    time.Time     `json:"finished_at"`
	Dir           string        `json:"dir"`
}

// Errored reports whether any run hit an infrastructure, budget or
// cancellation error.
func (t *TestOutcome) Errored() bool {
	for _, r := range t.Runs {
		if r.ErrorKind == KindInfra || r.ErrorKind == KindBudget || r.ErrorKind == KindCancelled {
			return true
		}
	}
	return false
}

// Finalize computes the pass rate and the verdict against the threshold.
func (t *TestOutcome) Finalize(now time.Time) {
	passed := 0
	for _, r := range t.Runs {
		if r.Passed {
			passed++
		}
	}
	if len(t.Runs) > 0 {
		t.PassRate = float64(passed) / float64(len(t.Runs))
	}
	t.Passed = len(t.Runs) > 0 && t.PassRate >= t.PassThreshold && !t.Errored()
	t.FinishedAt = now
}

// RunOutcome is one repeat of a test.
type RunOutcome struct {
	Index    int `json:"index"`
	Attempt  int `json:"attempt"`
	Attempts int `json:"attempts"`
	// Passed is set when the run completed and every assertion passed.
	Passed bool `json:"passed"`
	// Error and ErrorKind describe why a run did not complete.
	Error      string             `json:"error,omitempty"`
	ErrorKind  string             `json:"error_kind,omitempty"`
	StartedAt  time.Time          `json:"started_at"`
	Duration   time.Duration      `json:"duration"`
	Assertions []assert.Result    `json:"assertions"`
	Metrics    map[string]float64 `json:"metrics"`
	Transcript []assert.Turn      `json:"transcript"`
	ToolCalls  []target.ToolCall  `json:"tool_calls,omitempty"`
	Events     []target.Event     `json:"events,omitempty"`
	// Steps are a browser test's steps, in order, with their outcome.
	Steps      []StepRecord      `json:"steps,omitempty"`
	Artifacts  map[string]string `json:"artifacts,omitempty"`
	TraceIDs   []string          `json:"trace_ids,omitempty"`
	StopReason string            `json:"stop_reason,omitempty"`
	// Usage is tamesu's own model spend during this run. It is measured as
	// the change of the provider's meter, so it is exact when runs do not
	// overlap and approximate under --parallel.
	Usage model.Usage `json:"usage"`
	Dir   string      `json:"dir"`
}

// StepRecord is one browser step as it ran.
type StepRecord struct {
	Index int `json:"index"`
	// Kind is goal, expect or action.
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Success bool   `json:"success"`
	Reason  string `json:"reason,omitempty"`
	// Steps is the number of agent actions a goal took; Actions lists them.
	Steps      int             `json:"steps,omitempty"`
	Actions    []target.Action `json:"actions,omitempty"`
	Screenshot string          `json:"screenshot,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
	EndedAt    time.Time       `json:"ended_at"`
}

// FailedAssertions lists the assertions that did not pass.
func (r *RunOutcome) FailedAssertions() []assert.Result {
	var out []assert.Result
	for _, a := range r.Assertions {
		if !a.Pass {
			out = append(out, a)
		}
	}
	return out
}

// Summary is the whole invocation.
type Summary struct {
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     time.Time      `json:"finished_at"`
	OutDir         string         `json:"out_dir"`
	Tests          []*TestOutcome `json:"tests"`
	Usage          model.Usage    `json:"usage"`
	BudgetExceeded bool           `json:"budget_exceeded"`
	Version        string         `json:"tamesu_version,omitempty"`
}

// Counts returns how many tests passed, failed and errored.
func (s *Summary) Counts() (passed, failed, errored int) {
	for _, t := range s.Tests {
		switch {
		case t.Errored():
			errored++
		case t.Passed:
			passed++
		default:
			failed++
		}
	}
	return passed, failed, errored
}

// Duration is the wall-clock time of the invocation.
func (s *Summary) Duration() time.Duration { return s.FinishedAt.Sub(s.StartedAt) }

// ExitCode maps the summary to a process exit code (design.md §3): 4 when
// the budget stopped the run, 3 when any test hit an infrastructure error, 1
// when any test failed, else 0.
func (s *Summary) ExitCode() int {
	if s.BudgetExceeded {
		return 4
	}
	_, failed, errored := s.Counts()
	if errored > 0 {
		return 3
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// Reporter receives the events of a run. Methods may be called from several
// goroutines at once; implementations lock.
type Reporter interface {
	RunStarted(info RunInfo)
	TestStarted(t *TestOutcome)
	RunFinished(t *TestOutcome, r *RunOutcome)
	TestFinished(t *TestOutcome)
	Finish(ctx context.Context, s *Summary) error
}

// Multi fans events out to several reporters.
type Multi []Reporter

func (m Multi) RunStarted(info RunInfo) {
	for _, r := range m {
		r.RunStarted(info)
	}
}

func (m Multi) TestStarted(t *TestOutcome) {
	for _, r := range m {
		r.TestStarted(t)
	}
}

func (m Multi) RunFinished(t *TestOutcome, run *RunOutcome) {
	for _, r := range m {
		r.RunFinished(t, run)
	}
}

func (m Multi) TestFinished(t *TestOutcome) {
	for _, r := range m {
		r.TestFinished(t)
	}
}

// Finish runs every reporter's Finish and returns the first error.
func (m Multi) Finish(ctx context.Context, s *Summary) error {
	var first error
	for _, r := range m {
		if err := r.Finish(ctx, s); err != nil && first == nil {
			first = err
		}
	}
	return first
}
