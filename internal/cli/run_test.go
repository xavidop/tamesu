package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xavidop/mamori"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
)

// fakeModel says hello then stops, and judges by the rubric's wording.
type fakeModel struct {
	mu    sync.Mutex
	usage model.Usage
	cfg   cli.ProviderConfig
}

func (p *fakeModel) Simulate(_ context.Context, in model.SimInput) (model.SimOutput, error) {
	p.mu.Lock()
	p.usage.Calls++
	p.usage.CostUSD += 0.01
	p.mu.Unlock()
	if in.Turn >= 2 {
		return model.SimOutput{Say: "bye", Stop: true}, nil
	}
	return model.SimOutput{Say: "hello"}, nil
}

func (p *fakeModel) Judge(_ context.Context, req tassert.JudgeRequest) (tassert.JudgeVerdict, error) {
	return tassert.JudgeVerdict{Pass: strings.Contains(req.Rubric, "pass"), Reason: "judged"}, nil
}

func (p *fakeModel) Evaluate(context.Context, tassert.EvalRequest) (tassert.EvalVerdict, error) {
	return tassert.EvalVerdict{Pass: true}, nil
}

func (p *fakeModel) Usage() model.Usage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.usage
}

func (p *fakeModel) Close(context.Context) error { return nil }

// echoTarget answers "echo: <text>" and records the rendered Authorization header.
type echoTarget struct {
	spec runner.TargetSpec
	auth *string
}

func (e *echoTarget) Name() string { return e.spec.Name }

func (e *echoTarget) Start(_ context.Context, rc *target.RunContext) (target.Session, error) {
	if strings.HasPrefix(e.spec.Config.URL, "fail://") {
		return nil, errors.New("connection refused")
	}
	auth, err := rc.Render(e.spec.Config.Headers["Authorization"])
	if err != nil {
		return nil, err
	}
	*e.auth = auth
	return &echoSession{}, nil
}

type echoSession struct{ pending *target.AgentTurn }

func (s *echoSession) Send(_ context.Context, turn target.UserTurn) error {
	now := time.Now()
	s.pending = &target.AgentTurn{Text: "echo: " + turn.Text, StartedAt: now, EndedAt: now, Status: 200}
	return nil
}

func (s *echoSession) Receive(context.Context) (target.AgentTurn, error) {
	if s.pending == nil {
		return target.AgentTurn{}, errors.New("nothing pending")
	}
	t := *s.pending
	s.pending = nil
	return t, nil
}

func (s *echoSession) Close(context.Context) (target.Artifacts, error) {
	return target.Artifacts{}, nil
}

const runSuite = `suite: run-suite
secrets:
  TOKEN: fake://token
targets:
  api:
    type: rest
    url: http://agent
    headers: { Authorization: "Bearer {{secrets.TOKEN}}" }
    response: { text: $.reply }
  down:
    type: rest
    url: fail://agent
    response: { text: $.reply }
tests:
  - name: good
    target: api
    tags: [smoke]
    scenario: { goal: say hi }
    assert:
      - contains: "echo: hello"
      - judge: "should pass"
  - name: bad
    target: api
    scenario: { goal: say hi }
    assert:
      - not_contains: echo
  - name: broken
    target: down
    tags: [infra]
    scenario: { goal: say hi }
`

func runDeps(t *testing.T) (cli.Deps, *fakeModel, *string) {
	t.Helper()
	p := &fakeModel{}
	auth := new(string)
	deps := cli.Deps{
		Providers: []mamori.Provider{&fakeProvider{values: map[string]string{"token": "s3cret-token"}}},
		NewProvider: func(_ context.Context, cfg cli.ProviderConfig) (model.Provider, error) {
			p.cfg = cfg
			return p, nil
		},
		Targets: map[string]cli.TargetFactory{"rest": func(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
			return &echoTarget{spec: spec, auth: auth}, nil
		}},
		Now: func() time.Time { return time.Date(2026, 10, 31, 14, 20, 0, 0, time.UTC) },
	}
	return deps, p, auth
}

func TestRunEndToEnd(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"tamesu.yaml":  "version: 1\nsuites: [\"s/**/*.yaml\"]\nreporters: [console, junit]\nbudgets: { max_cost_usd: 5 }\n",
		"s/suite.yaml": runSuite,
	})
	deps, p, auth := runDeps(t)
	out := filepath.Join(dir, "out")
	code, stdout, stderr := run(t, nil, deps, "run", "--config", filepath.Join(dir, "tamesu.yaml"), "--filter", "good", "--out", out)
	assert.Equal(t, cli.ExitOK, code, stderr)
	assert.Contains(t, stdout, "PASS   run-suite / good")
	assert.Contains(t, stdout, "1 passed, 0 failed, 0 errors")
	assert.Equal(t, "Bearer s3cret-token", *auth, "secrets are revealed inside the transport")
	assert.NotContains(t, stdout, "s3cret-token")
	for _, f := range []string{"summary.json", "junit.xml", "tests/good/run-1/result.json", "tests/good/run-1/transcript.json"} {
		_, err := os.Stat(filepath.Join(out, f))
		assert.NoError(t, err, f)
	}
	b, _ := os.ReadFile(filepath.Join(out, "tests", "good", "run-1", "result.json"))
	assert.NotContains(t, string(b), "s3cret-token", "artifacts never hold a secret")
	assert.Equal(t, 5.0, p.cfg.MaxCostUSD)
	v, ok := p.cfg.Credentials("TOKEN")
	assert.True(t, ok)
	assert.Equal(t, "s3cret-token", v)

	// A failing test exits 1; an unreachable target exits 3.
	code, stdout, _ = run(t, nil, deps, "run", "--config", filepath.Join(dir, "tamesu.yaml"), "--filter", "bad", "--out", filepath.Join(dir, "out2"))
	assert.Equal(t, cli.ExitFailed, code)
	assert.Contains(t, stdout, "FAIL   run-suite / bad")
	assert.Contains(t, stdout, `x not_contains "echo": agent turn 1 contains "echo"`)
	code, stdout, _ = run(t, nil, deps, "run", "--config", filepath.Join(dir, "tamesu.yaml"), "--tags", "infra", "--out", filepath.Join(dir, "out3"))
	assert.Equal(t, cli.ExitInfra, code)
	assert.Contains(t, stdout, "ERROR  run-suite / broken")
}

func TestRunErrors(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"tamesu.yaml":  "version: 1\nsuites: [\"s/**/*.yaml\"]\n",
		"s/suite.yaml": runSuite,
	})
	deps, _, _ := runDeps(t)
	cfg := filepath.Join(dir, "tamesu.yaml")

	code, _, stderr := run(t, nil, deps, "run", "--config", cfg, "--filter", "nothing-matches")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "no tests match the selection")

	code, _, stderr = run(t, nil, deps, "run", "--config", cfg, "--reporter", "fax", "--filter", "good")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, `unknown reporter "fax"`)

	code, _, stderr = run(t, nil, deps, "run", "--config", cfg, "--reporter", "slack", "--filter", "good")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "the slack reporter needs slack.webhook")

	code, _, stderr = run(t, nil, deps, "run", "--config", cfg, "--mode", "video")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "--mode must be text or audio")

	// A secret that cannot be resolved is a config error, before anything runs.
	missing := writeProject(t, map[string]string{"tamesu.yaml": "version: 1\nsuites: [\"s/**/*.yaml\"]\n", "s/suite.yaml": strings.ReplaceAll(runSuite, "fake://token", "fake://nope")})
	code, _, stderr = run(t, nil, deps, "run", "--config", filepath.Join(missing, "tamesu.yaml"), "--filter", "good", "--out", filepath.Join(missing, "out"))
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "secret TOKEN: not found")

	// No model provider in this build: conversational tests cannot run.
	noAI := deps
	noAI.NewProvider = nil
	code, stdout, _ := run(t, nil, noAI, "run", "--config", cfg, "--filter", "good", "--out", filepath.Join(dir, "noai"))
	assert.Equal(t, cli.ExitInfra, code)
	assert.Contains(t, stdout, "no simulator model is available")

	// A target type this build does not link.
	lk := writeProject(t, map[string]string{"tamesu.yaml": "version: 1\nsuites: [\"s/**/*.yaml\"]\n", "s/suite.yaml": "suite: v\ntargets:\n  voice: { type: livekit, url: ws://x }\ntests:\n  - { name: t, target: voice, scenario: { goal: g } }\n"})
	code, stdout, _ = run(t, nil, deps, "run", "--config", filepath.Join(lk, "tamesu.yaml"), "--out", filepath.Join(lk, "out"))
	assert.Equal(t, cli.ExitInfra, code)
	assert.Contains(t, stdout, "target type livekit is not available in this build")
}

func TestRunDefaultOutDir(t *testing.T) {
	dir := writeProject(t, map[string]string{"tamesu.yaml": "version: 1\nsuites: [\"s/**/*.yaml\"]\nreporters: [json]\n", "s/suite.yaml": runSuite})
	deps, _, _ := runDeps(t)
	wd, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	code, _, stderr := run(t, nil, deps, "run", "--filter", "good")
	assert.Equal(t, cli.ExitOK, code, stderr)
	_, err := os.Stat(filepath.Join(dir, ".tamesu", "runs", "2026-10-31T14-20-00", "summary.json"))
	assert.NoError(t, err)
}

func TestRunLabelsFlag(t *testing.T) {
	// --label is parsed as KEY=VALUE and rejected otherwise.
	deps := cli.Deps{LookupEnv: func(string) (string, bool) { return "", false }}
	code, _, stderr := run(t, nil, deps, "run", "--label", "nokey", "--filter", "x")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "want KEY=VALUE")
}
