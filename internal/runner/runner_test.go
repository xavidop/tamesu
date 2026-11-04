package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/report"
	"github.com/xavidop/tamesu/target"
)

// fakeProvider says "hello" then stops, judges by the rubric's wording and
// charges a fixed cost per call.
type fakeProvider struct {
	mu          sync.Mutex
	usage       model.Usage
	costPerCall float64
	lastGoal    string
}

func (p *fakeProvider) Simulate(_ context.Context, in model.SimInput) (model.SimOutput, error) {
	p.charge()
	p.mu.Lock()
	p.lastGoal = in.Scenario.Goal
	p.mu.Unlock()
	if in.Turn >= 2 {
		return model.SimOutput{Say: "thanks, bye", Stop: true, Reason: "done", TraceID: "sim-2"}, nil
	}
	return model.SimOutput{Say: "hello " + in.Persona.Name, TraceID: "sim-1"}, nil
}

func (p *fakeProvider) Judge(_ context.Context, req tassert.JudgeRequest) (tassert.JudgeVerdict, error) {
	p.charge()
	pass := strings.Contains(req.Rubric, "pass")
	return tassert.JudgeVerdict{Pass: pass, Score: 1, Reason: "judged " + req.Rubric, TraceID: "judge-1"}, nil
}

func (p *fakeProvider) Evaluate(context.Context, tassert.EvalRequest) (tassert.EvalVerdict, error) {
	return tassert.EvalVerdict{Pass: true, Score: 1}, nil
}

func (p *fakeProvider) Usage() model.Usage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.usage
}

func (p *fakeProvider) Close(context.Context) error { return nil }

func (p *fakeProvider) charge() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usage.Calls++
	p.usage.InputTokens += 100
	p.usage.OutputTokens += 10
	p.usage.CostUSD += p.costPerCall
}

// echoTarget answers "echo: <text>". Its URL selects a behaviour:
// fail://start fails Start, fail://start-once fails the first Start only,
// slow:// sleeps in Receive, flaky:// answers "odd" on odd runs.
type echoTarget struct {
	spec   TargetSpec
	starts *atomic.Int32
}

func (e *echoTarget) Name() string { return e.spec.Name }

func (e *echoTarget) Start(ctx context.Context, rc *target.RunContext) (target.Session, error) {
	n := e.starts.Add(1)
	switch e.spec.Config.URL {
	case "fail://start":
		return nil, errors.New("connection refused")
	case "fail://start-once":
		if n == 1 {
			return nil, errors.New("connection refused")
		}
	}
	url, err := rc.Render(e.spec.Config.URL)
	if err != nil {
		return nil, err
	}
	auth, _ := rc.Render(e.spec.Config.Headers["Authorization"])
	delegate, _ := rc.Render(e.spec.Config.Headers["X-Delegate"])
	return &echoSession{url: url, auth: auth, delegate: delegate, run: rc.RunIndex, slow: strings.HasPrefix(e.spec.Config.URL, "slow://"), flaky: strings.HasPrefix(e.spec.Config.URL, "flaky://")}, nil
}

type echoSession struct {
	url, auth string
	// delegate is a remote agent's URL the session sends every user turn to
	// before answering (an orchestrator).
	delegate string
	run      int
	slow     bool
	flaky    bool
	pending  *target.AgentTurn
}

// delegateTo sends the text to the remote agent as an A2A message.
func (s *echoSession) delegateTo(ctx context.Context, text string) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "method": "message/send", "params": map[string]any{
		"message": map[string]any{"kind": "message", "messageId": "m", "role": "user", "parts": []any{map[string]any{"kind": "text", "text": text}}}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.delegate+"/a2a", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (s *echoSession) Send(ctx context.Context, turn target.UserTurn) error {
	start := time.Now()
	if s.delegate != "" {
		if err := s.delegateTo(ctx, turn.Text); err != nil {
			return err
		}
	}
	if s.slow {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	text := "echo: " + turn.Text
	if s.flaky && s.run%2 == 1 {
		text = "odd run"
	}
	s.pending = &target.AgentTurn{Text: text, StartedAt: start, EndedAt: time.Now(), Status: 200,
		Raw:       map[string]any{"reply": text, "url": s.url, "auth": s.auth},
		ToolCalls: []target.ToolCall{{Name: "lookup", Args: map[string]any{"q": turn.Text}}}}
	return nil
}

func (s *echoSession) Receive(ctx context.Context) (target.AgentTurn, error) {
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("nothing pending")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

func (s *echoSession) Close(context.Context) (target.Artifacts, error) {
	return target.Artifacts{Metrics: map[string]float64{"custom_metric": 42}, Events: []target.Event{{Kind: tassert.EventCallEnded, Detail: "agent"}}}, nil
}

func factory(starts *atomic.Int32) TargetFactory {
	return func(_ context.Context, spec TargetSpec) (target.Target, error) {
		if spec.Config.Type != config.TargetREST {
			return nil, errors.New("target type " + spec.Config.Type + " is not available in this build")
		}
		return &echoTarget{spec: spec, starts: starts}, nil
	}
}

func suite(url string, tests ...config.Test) *config.Suite {
	return &config.Suite{
		Suite: "s", Path: "s.yaml", Tags: []string{"smoke"}, Vars: map[string]string{"who": "Marta"},
		Secrets: map[string]string{"TOKEN": "env:X"},
		Targets: map[string]config.Target{"api": {Type: config.TargetREST, URL: url, Headers: map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"}}},
		Tests:   tests,
	}
}

func ptr[T any](v T) *T { return &v }

func convTest(name string, asserts ...config.Assertion) config.Test {
	return config.Test{Name: name, Target: "api", Persona: &config.Persona{Name: "{{vars.who}}"}, Scenario: &config.Scenario{Goal: "say hi using {{secrets.TOKEN}}", MaxTurns: 4}, Assert: asserts}
}

func newOptions(t *testing.T, s *config.Suite, p *fakeProvider) (Options, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	out := filepath.Join(t.TempDir(), "run")
	return Options{
		Project:  config.DefaultProject(),
		Suites:   []*config.Suite{s},
		OutDir:   out,
		Parallel: 2,
		Targets:  factory(new(atomic.Int32)),
		Provider: p,
		Vars:     map[string]string{"who": "Marta"},
		Reporter: report.Multi{report.NewConsole(&buf, true), report.NewJSON(out), report.NewJUnit(out)},
	}, &buf
}

func TestRunPassesAndFails(t *testing.T) {
	tassert.Register("always", func() tassert.Assertion {
		return tassert.Func("always", func(context.Context, *tassert.RunResult) tassert.Result {
			return tassert.Result{Pass: true, Reason: "ok"}
		})
	})
	s := suite("http://agent",
		convTest("ok",
			config.Assertion{Contains: ptr("echo: hello")},
			config.Assertion{Judge: &config.Judge{Rubric: "should pass"}},
			config.Assertion{ToolCalled: &config.ToolCall{Name: "lookup", Args: map[string]any{"q": "thanks, bye"}}},
			config.Assertion{Metric: &config.MetricAssertion{Name: "turns", Eq: ptr(2.0)}},
			config.Assertion{Metric: &config.MetricAssertion{Name: "custom_metric", Gt: ptr(40.0)}},
			config.Assertion{Status: ptr(200)},
			config.Assertion{JSONPath: &config.JSONPathAssertion{Path: "$.auth", Equals: "Bearer {{secrets.TOKEN}}"}},
			config.Assertion{CallEndedBy: config.StringList{"agent"}},
			config.Assertion{Custom: ptr("always")},
			config.Assertion{Contains: ptr("bye"), OnTurn: config.OnTurn{Last: true}},
		),
		convTest("bad",
			config.Assertion{NotContains: ptr("echo")},
			config.Assertion{Judge: &config.Judge{Rubric: "should fail"}},
		),
	)
	p := &fakeProvider{costPerCall: 0.01}
	o, buf := newOptions(t, s, p)
	o.Parallel = 1 // per-run usage is exact only when runs do not overlap
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	require.Len(t, sum.Tests, 2)
	ok, bad := sum.Tests[0], sum.Tests[1]
	assert.True(t, ok.Passed, "%+v", ok.Runs[0].Assertions)
	for _, a := range ok.Runs[0].Assertions {
		assert.True(t, a.Pass, "%s: %s", a.Name, a.Reason)
	}
	assert.Equal(t, "echo: hello Marta", ok.Runs[0].Transcript[1].Text, "persona name rendered through vars")
	assert.Equal(t, "say hi using {{secrets.TOKEN}}", p.lastGoal, "the model never sees a secret value")
	assert.False(t, bad.Passed)
	assert.Equal(t, 1, sum.ExitCode())
	assert.Equal(t, []string{"sim-1", "sim-2", "judge-1"}, ok.Runs[0].TraceIDs)
	assert.Equal(t, 2.0, ok.Runs[0].Metrics["turns"])
	assert.Contains(t, ok.Runs[0].Metrics, "turn_latency_ms_p95")
	assert.Equal(t, 3, ok.Runs[0].Usage.Calls, "2 simulator calls and 1 judge")
	assert.Equal(t, 6, sum.Usage.Calls)
	assert.InDelta(t, 0.06, sum.Usage.CostUSD, 1e-9)
	assert.Equal(t, "simulator: done", ok.Runs[0].StopReason)

	// Run directory layout.
	for _, f := range []string{"summary.json", "junit.xml", "tests/ok/run-1/result.json", "tests/ok/run-1/transcript.json", "tests/bad/run-1/result.json"} {
		_, err := os.Stat(filepath.Join(o.OutDir, f))
		assert.NoError(t, err, f)
	}
	b, err := os.ReadFile(filepath.Join(o.OutDir, "tests", "bad", "run-1", "result.json"))
	require.NoError(t, err)
	var ro report.RunOutcome
	require.NoError(t, json.Unmarshal(b, &ro))
	assert.False(t, ro.Passed)
	assert.Len(t, ro.FailedAssertions(), 2)
	assert.Contains(t, buf.String(), "PASS   s / ok")
	assert.Contains(t, buf.String(), "FAIL   s / bad")
	assert.Contains(t, buf.String(), "judged should fail")
}

func TestRunQuarantine(t *testing.T) {
	s := suite("http://agent",
		convTest("flaky", config.Assertion{NotContains: ptr("echo")}),
		convTest("expired", config.Assertion{NotContains: ptr("echo")}),
		convTest("solid", config.Assertion{Contains: ptr("echo")}),
	)
	s.Tests[0].Quarantine = &config.Quarantine{Reason: "SUP-412", Until: "2026-12-31"}
	s.Tests[1].Quarantine = &config.Quarantine{Reason: "SUP-1", Until: "2026-01-31"}
	o, buf := newOptions(t, s, &fakeProvider{})
	o.Now = func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	require.Len(t, sum.Tests, 3)
	flaky, expired := sum.Tests[0], sum.Tests[1]
	assert.False(t, flaky.Passed)
	require.NotNil(t, flaky.Quarantine)
	assert.Equal(t, "SUP-412", flaky.Quarantine.Reason)
	assert.True(t, flaky.Quarantined())
	assert.Nil(t, expired.Quarantine, "an expired quarantine is not carried into the run")
	assert.False(t, expired.Passed)
	assert.Equal(t, 1, sum.ExitCode(), "the expired one still fails the run")
	assert.Contains(t, buf.String(), "QUAR   s / flaky")
	assert.Contains(t, buf.String(), "FAIL   s / expired")

	sum.Tests = []*report.TestOutcome{flaky, sum.Tests[2]}
	assert.Equal(t, 0, sum.ExitCode(), "a quarantined failure alone does not fail the run")
}

func TestRunShard(t *testing.T) {
	s := suite("http://agent",
		convTest("a", config.Assertion{Contains: ptr("echo")}),
		convTest("b", config.Assertion{Contains: ptr("echo")}),
		convTest("c", config.Assertion{Contains: ptr("echo")}),
		convTest("d", config.Assertion{Contains: ptr("echo")}),
		convTest("e", config.Assertion{Contains: ptr("echo")}),
	)
	var names [][]string
	for i := 1; i <= 2; i++ {
		o, _ := newOptions(t, s, &fakeProvider{})
		o.Select.Shard = config.Shard{Index: i, Total: 2}
		sum, err := Run(context.Background(), o)
		require.NoError(t, err)
		var ns []string
		for _, to := range sum.Tests {
			ns = append(ns, to.Name)
		}
		names = append(names, ns)
	}
	assert.Equal(t, [][]string{{"a", "c", "e"}, {"b", "d"}}, names)

	o, _ := newOptions(t, s, &fakeProvider{})
	o.Select.Shard = config.Shard{Index: 3, Total: 3}
	o.Select.Filter = regexp.MustCompile("^a$")
	_, err := Run(context.Background(), o)
	assert.ErrorIs(t, err, ErrEmptyShard, "one test selected, shard 3/3 is empty")
	o.Select.Filter = regexp.MustCompile("^nothing$")
	_, err = Run(context.Background(), o)
	assert.ErrorIs(t, err, ErrNoTests)
}

func TestRunFailFast(t *testing.T) {
	s := suite("http://agent", convTest("bad", config.Assertion{NotContains: ptr("echo")}))
	s.Tests[0].Repeat = ptr(3)
	o, buf := newOptions(t, s, &fakeProvider{})
	o.Parallel = 1
	o.FailFast = true
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	require.Len(t, sum.Tests, 1)
	bad := sum.Tests[0]
	assert.False(t, bad.Passed)
	assert.False(t, bad.Skipped(), "one run completed")
	ran, skipped := 0, 0
	for _, r := range bad.Runs {
		if r.ErrorKind == report.KindSkipped {
			skipped++
		} else {
			ran++
		}
	}
	assert.Equal(t, 1, ran, "the first run fails and stops the rest")
	assert.Equal(t, 2, skipped)
	assert.Equal(t, 0.0, bad.PassRate)
	assert.Equal(t, report.StoppedFailFast, sum.StoppedEarly)
	assert.Equal(t, 1, sum.ExitCode())
	assert.Contains(t, buf.String(), "stopped after the first failure (--fail-fast)")

	// Quarantined failures do not stop the run.
	s.Tests[0].Quarantine = &config.Quarantine{Reason: "SUP-1"}
	o, _ = newOptions(t, s, &fakeProvider{})
	o.Parallel = 1
	o.FailFast = true
	sum, err = Run(context.Background(), o)
	require.NoError(t, err)
	assert.Equal(t, "", sum.StoppedEarly)
	assert.Len(t, sum.Tests[0].Runs, 3)
	for _, r := range sum.Tests[0].Runs {
		assert.NotEqual(t, report.KindSkipped, r.ErrorKind)
	}
}

func TestRunRepeatsAndThreshold(t *testing.T) {
	s := suite("flaky://agent", config.Test{Name: "flaky", Target: "api", Repeat: ptr(4), PassThreshold: ptr(0.5), Scenario: &config.Scenario{Goal: "g"}, Assert: []config.Assertion{{Contains: ptr("echo")}}})
	o, _ := newOptions(t, s, &fakeProvider{})
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	to := sum.Tests[0]
	require.Len(t, to.Runs, 4)
	assert.Equal(t, 0.5, to.PassRate)
	assert.True(t, to.Passed)
	assert.Equal(t, 0, sum.ExitCode())
	for i, r := range to.Runs {
		assert.Equal(t, i+1, r.Index)
		assert.Equal(t, filepath.Join(to.Dir, "run-"+string(rune('1'+i))), r.Dir)
	}

	o.Repeat = 1
	sum, err = Run(context.Background(), o)
	require.NoError(t, err)
	assert.Len(t, sum.Tests[0].Runs, 1, "--repeat overrides the test")
}

func TestRunRetriesInfrastructureErrors(t *testing.T) {
	s := suite("fail://start-once", config.Test{Name: "retry", Target: "api", Retries: ptr(1), Scenario: &config.Scenario{Goal: "g"}, Assert: []config.Assertion{{Contains: ptr("echo")}}})
	o, _ := newOptions(t, s, &fakeProvider{})
	o.Parallel = 1
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	r := sum.Tests[0].Runs[0]
	assert.True(t, r.Passed)
	assert.Equal(t, 2, r.Attempt)
	assert.Equal(t, 0, sum.ExitCode())

	s = suite("fail://start", config.Test{Name: "down", Target: "api", Scenario: &config.Scenario{Goal: "g"}})
	o, buf := newOptions(t, s, &fakeProvider{})
	sum, err = Run(context.Background(), o)
	require.NoError(t, err)
	r = sum.Tests[0].Runs[0]
	assert.Equal(t, report.KindInfra, r.ErrorKind)
	assert.Equal(t, "start api: connection refused", r.Error)
	assert.True(t, sum.Tests[0].Errored())
	assert.Equal(t, 3, sum.ExitCode())
	assert.Contains(t, buf.String(), "ERROR  s / down")
}

func TestRunTimeout(t *testing.T) {
	s := suite("slow://agent", config.Test{Name: "slow", Target: "api", Timeout: ptr(config.Duration(50 * time.Millisecond)), Scenario: &config.Scenario{Goal: "g"}})
	o, _ := newOptions(t, s, &fakeProvider{})
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	r := sum.Tests[0].Runs[0]
	assert.Equal(t, report.KindTimeout, r.ErrorKind)
	assert.Contains(t, r.Error, "timed out after 50ms")
	assert.False(t, sum.Tests[0].Passed)
	assert.Equal(t, 1, sum.ExitCode(), "a timeout is a failure, not an infrastructure error")
}

func TestRunBudget(t *testing.T) {
	s := suite("http://agent", convTest("a"), convTest("b"), convTest("c"))
	o, _ := newOptions(t, s, &fakeProvider{costPerCall: 1})
	o.Parallel = 1
	o.MaxCostUSD = 2.5
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.True(t, sum.BudgetExceeded)
	assert.Equal(t, 4, sum.ExitCode())
	kinds := map[string]int{}
	for _, to := range sum.Tests {
		kinds[to.Runs[0].ErrorKind]++
	}
	assert.Equal(t, 2, kinds[""], "two tests complete (2.0 then 4.0 USD) before the cap is noticed")
	assert.Equal(t, 1, kinds[report.KindBudget], "the third never starts")
}

// slowTarget answers after a delay and records how many sessions are open at once.
type slowTarget struct {
	open, peak atomic.Int32
}

func (s *slowTarget) Name() string { return "slow" }

func (s *slowTarget) Start(context.Context, *target.RunContext) (target.Session, error) {
	n := s.open.Add(1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	return &slowSession{t: s}, nil
}

type slowSession struct {
	t       *slowTarget
	pending *target.AgentTurn
}

func (s *slowSession) Send(ctx context.Context, turn target.UserTurn) error {
	time.Sleep(30 * time.Millisecond)
	now := time.Now()
	s.pending = &target.AgentTurn{Text: "echo: " + turn.Text, StartedAt: now, EndedAt: now}
	return nil
}

func (s *slowSession) Receive(context.Context) (target.AgentTurn, error) {
	t := *s.pending
	s.pending = nil
	return t, nil
}

func (s *slowSession) Close(context.Context) (target.Artifacts, error) {
	s.t.open.Add(-1)
	return target.Artifacts{}, nil
}

func TestRunLimitsPerTargetType(t *testing.T) {
	slow := &slowTarget{}
	s := &config.Suite{Suite: "s", Path: "s.yaml", Targets: map[string]config.Target{"voice": {Type: config.TargetLiveKit, URL: "ws://x"}}}
	for i := 0; i < 6; i++ {
		s.Tests = append(s.Tests, config.Test{Name: fmt.Sprintf("t%d", i), Target: "voice", Scenario: &config.Scenario{Goal: "g"}})
	}
	o, _ := newOptions(t, s, &fakeProvider{})
	o.Parallel = 6
	o.Limits = map[string]int{config.TargetLiveKit: 2}
	o.Targets = func(context.Context, TargetSpec) (target.Target, error) { return slow, nil }
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Equal(t, 0, sum.ExitCode())
	assert.LessOrEqual(t, slow.peak.Load(), int32(2), "livekit runs are capped by max_concurrent_rooms")
}

func TestRunSelection(t *testing.T) {
	s := suite("http://agent", convTest("alpha"), convTest("beta"))
	o, _ := newOptions(t, s, &fakeProvider{})
	o.Select = Selection{Filter: regexp.MustCompile("^al")}
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	require.Len(t, sum.Tests, 1)
	assert.Equal(t, "alpha", sum.Tests[0].Name)

	o.Select = Selection{Tags: []string{"nightly"}}
	_, err = Run(context.Background(), o)
	assert.ErrorIs(t, err, ErrNoTests)
}

func TestRunWithoutProvider(t *testing.T) {
	s := suite("http://agent", convTest("x", config.Assertion{Judge: &config.Judge{Rubric: "pass"}}))
	o, _ := newOptions(t, s, nil)
	o.Provider = nil
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	r := sum.Tests[0].Runs[0]
	assert.Equal(t, report.KindInfra, r.ErrorKind)
	assert.Contains(t, r.Error, "no simulator model is available")
	assert.Equal(t, 3, sum.ExitCode())
}

func TestRunUnavailableTargetType(t *testing.T) {
	s := &config.Suite{Suite: "s", Path: "s.yaml", Targets: map[string]config.Target{"voice": {Type: config.TargetLiveKit, URL: "ws://x"}}, Tests: []config.Test{convTestOn("voice")}}
	o, _ := newOptions(t, s, &fakeProvider{})
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	r := sum.Tests[0].Runs[0]
	assert.Equal(t, report.KindInfra, r.ErrorKind)
	assert.Equal(t, "target voice: target type livekit is not available in this build", r.Error)
}

func convTestOn(target string) config.Test {
	t := convTest("v")
	t.Target = target
	return t
}

func TestBuild(t *testing.T) {
	tests := []struct {
		name string
		in   config.Assertion
		want string
		err  string
	}{
		{"judge", config.Assertion{Judge: &config.Judge{Rubric: "r", Scale: "1-5", Min: 3}}, `judge "r"`, ""},
		{"regex", config.Assertion{Regex: ptr(`\d+`)}, `regex "\\d+"`, ""},
		{"bad regex", config.Assertion{Regex: ptr(`(`)}, "", "regex"},
		{"json_path", config.Assertion{JSONPath: &config.JSONPathAssertion{Path: "$.a", Equals: 1}}, "json_path $.a == 1", ""},
		{"metric", config.Assertion{Metric: &config.MetricAssertion{Name: "m", Lte: ptr(3.0)}}, "metric m <= 3", ""},
		{"turns", config.Assertion{Turns: &config.IntCompare{Lt: ptr(5)}}, "turns < 5", ""},
		{"dtmf", config.Assertion{DTMFReceived: &config.DTMFAssertion{Equals: "12#"}}, "dtmf_received 12#", ""},
		{"transferred", config.Assertion{TransferredTo: &config.Matches{Matches: "^sip:"}}, `transferred_to "^sip:"`, ""},
		{"evaluator", config.Assertion{Evaluator: &config.Evaluator{Name: "e", Min: 0.5}}, "evaluator e", ""},
		{"on_turn", config.Assertion{Contains: ptr("x"), OnTurn: config.OnTurn{N: 2}}, `contains "x" (turn 2)`, ""},
		{"unknown custom", config.Assertion{Custom: ptr("nope")}, "", "not registered"},
		{"no kind", config.Assertion{}, "", "exactly one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := Build(tt.in)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, a.Name())
		})
	}
}

func TestSlugify(t *testing.T) {
	assert.Equal(t, "refund-policy-question", slugify("Refund policy question!"))
	assert.Equal(t, "test", slugify("***"))
	assert.Len(t, slugify(strings.Repeat("a", 100)), 60)
}

func TestRunLoadMode(t *testing.T) {
	s := suite("echo://agent", convTest("load", config.Assertion{Contains: ptr("echo")}))
	o, _ := newOptions(t, s, &fakeProvider{})
	o.Load = 3
	o.LoadDuration = 300 * time.Millisecond
	started := time.Now()
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	require.Len(t, sum.Tests, 1)
	to := sum.Tests[0]
	assert.GreaterOrEqual(t, time.Since(started), 300*time.Millisecond, "the users loop for the duration")
	assert.Greater(t, len(to.Runs), 3, "each user ran more than once")
	assert.Equal(t, 3, to.Repeat)
	assert.True(t, to.Passed)
	for i, r := range to.Runs {
		assert.Equal(t, i+1, r.Index, "runs are numbered in order")
		assert.True(t, r.Passed)
	}
	require.NotNil(t, to.Load)
	assert.Equal(t, 3, to.Load.Users)
	assert.Equal(t, len(to.Runs), to.Load.Runs)
	assert.Greater(t, to.Load.RunsPerSecond, 0.0)
	assert.Equal(t, 0.0, to.Load.ErrorRate)
	assert.Greater(t, to.Load.TurnLatencyMs.Count, 0)
	assert.Equal(t, 0, sum.ExitCode())

	o.LoadDuration = 0
	sum, err = Run(context.Background(), o)
	require.NoError(t, err)
	assert.Len(t, sum.Tests[0].Runs, 3, "without a duration every user runs once")
}

// Ground implements assert.Grounder: every claim is supported.
func (f *fakeProvider) Ground(_ context.Context, req tassert.GroundRequest) (tassert.GroundVerdict, error) {
	return tassert.GroundVerdict{Claims: []tassert.Claim{{Text: "fake claim", Supported: true, Evidence: "(fake)"}}, Reason: "fake grounder"}, nil
}

// Extract implements model.Extractor: the first word with a digit.
func (f *fakeProvider) Extract(_ context.Context, req model.ExtractRequest) (model.ExtractResult, error) {
	for _, w := range strings.Fields(req.Text) {
		if strings.ContainsAny(w, "0123456789") {
			return model.ExtractResult{Found: true, Value: w}, nil
		}
	}
	return model.ExtractResult{}, nil
}

func TestRunSessionOpenForAnyTarget(t *testing.T) {
	// session.open runs once per run before the target starts; its captures
	// render into the target's configuration whatever the target type.
	var opened int
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opened++
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"call": map[string]any{"id": "call-7", "token": "tok-abc"}})
	}))
	t.Cleanup(srv.Close)
	s := suite("http://agent/{{session.call_id}}", convTest("opens", config.Assertion{JSONPath: &config.JSONPathAssertion{Path: "$.url", Equals: "http://agent/call-7"}}, config.Assertion{JSONPath: &config.JSONPathAssertion{Path: "$.auth", Equals: "Bearer tok-abc"}}))
	s.Targets["api"] = config.Target{Type: config.TargetREST, URL: "http://agent/{{session.call_id}}", Headers: map[string]string{"Authorization": "Bearer {{session.token}}"},
		Session: &config.RESTSession{Open: &config.RESTOpen{URL: srv.URL + "/calls", Headers: map[string]string{"Authorization": "Bearer {{vars.who}}"}, Body: map[string]any{"user": "{{session}}", "who": "{{vars.who}}"}, Capture: map[string]string{"call_id": "$.call.id", "token": "$.call.token"}}}}
	opts, buf := newOptions(t, s, &fakeProvider{})
	sum, err := Run(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, sum.Tests, 1)
	assert.True(t, sum.Tests[0].Passed, buf.String())
	assert.Equal(t, 1, opened)
	assert.Equal(t, "Bearer Marta", gotAuth, "the open request renders templates")
	assert.Equal(t, "Marta", gotBody["who"])
	assert.NotEmpty(t, gotBody["user"], "{{session}} is the run's session id")

	// A refused open fails the run before the target starts.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusUnauthorized) }))
	t.Cleanup(bad.Close)
	s2 := suite("http://agent", convTest("refused", config.Assertion{Contains: ptr("echo")}))
	s2.Targets["api"] = config.Target{Type: config.TargetREST, URL: "http://agent", Session: &config.RESTSession{Open: &config.RESTOpen{URL: bad.URL}}}
	opts2, _ := newOptions(t, s2, &fakeProvider{})
	sum2, err := Run(context.Background(), opts2)
	require.NoError(t, err)
	assert.False(t, sum2.Tests[0].Passed)
	assert.Contains(t, sum2.Tests[0].Runs[0].Error, "session.open")
	assert.Contains(t, sum2.Tests[0].Runs[0].Error, "HTTP 401")
}

func TestRunRemoteAgentsAndCounterpart(t *testing.T) {
	s := suite("http://agent",
		convTest("delegates",
			config.Assertion{DelegatedTo: &config.Delegation{Agent: "billing", Contains: "bye"}},
			config.Assertion{DelegatedTo: &config.Delegation{Agent: "billing", Regex: "(?i)^hello"}},
			config.Assertion{Metric: &config.MetricAssertion{Name: "delegations", Eq: ptr(2.0)}},
			config.Assertion{ToolNotCalled: &config.ToolCall{Name: "shipping"}},
		),
		config.Test{Name: "two agents", Target: "api", Counterpart: &config.Counterpart{Target: "api", Opening: "We offer 500 units at 12 euros"}, Scenario: &config.Scenario{MaxTurns: 2},
			Assert: []config.Assertion{{Contains: ptr("echo: echo: We offer 500 units")}, {Metric: &config.MetricAssertion{Name: "turns", Eq: ptr(2.0)}}}},
	)
	s.Targets["api"] = config.Target{Type: config.TargetREST, URL: "http://agent", Headers: map[string]string{"X-Delegate": "{{remote.billing.url}}"}}
	s.RemoteAgents = map[string]config.RemoteAgent{"billing": {Skills: []config.RemoteSkill{{ID: "refund"}}, Reply: "Refund issued, reference RF-889",
		Replies: []config.RemoteReply{{When: "(?i)bye", Say: "Goodbye from billing"}}}}
	opts, buf := newOptions(t, s, &fakeProvider{})
	sum, err := Run(context.Background(), opts)
	require.NoError(t, err)
	byName := map[string]*report.TestOutcome{}
	for _, to := range sum.Tests {
		byName[to.Name] = to
	}
	require.NotNil(t, byName["delegates"])
	assert.True(t, byName["delegates"].Passed, buf.String())
	run := byName["delegates"].Runs[0]
	var delegations []target.ToolCall
	for _, c := range run.ToolCalls {
		if c.Kind == target.ToolCallDelegation {
			delegations = append(delegations, c)
		}
	}
	require.Len(t, delegations, 2, "one per user turn")
	assert.Equal(t, "billing", delegations[0].Name)
	assert.Equal(t, "Refund issued, reference RF-889", delegations[0].Result)
	assert.Equal(t, "Goodbye from billing", delegations[1].Result, "the when rule matched the goodbye")
	kinds := 0
	for _, e := range run.Events {
		if e.Kind == "delegated" {
			kinds++
		}
	}
	assert.Equal(t, 2, kinds)

	two := byName["two agents"]
	require.NotNil(t, two)
	assert.True(t, two.Passed, buf.String())
	assert.Equal(t, "max_turns", two.Runs[0].StopReason)
	require.Len(t, two.Runs[0].Transcript, 4)
	assert.Equal(t, "We offer 500 units at 12 euros", two.Runs[0].Transcript[0].Text, "the opening")
	assert.Equal(t, "echo: echo: We offer 500 units at 12 euros", two.Runs[0].Transcript[2].Text, "the counterpart answered the agent's words")
	assert.Equal(t, "counterpart", two.Runs[0].Events[0].Kind)
}
