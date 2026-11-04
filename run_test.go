package tamesu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

type fakeProvider struct{ calls int }

func (p *fakeProvider) Simulate(_ context.Context, in model.SimInput) (model.SimOutput, error) {
	p.calls++
	if in.Turn >= 2 {
		return model.SimOutput{Say: "bye", Stop: true}, nil
	}
	return model.SimOutput{Say: "can I return shoes?"}, nil
}

func (p *fakeProvider) Judge(_ context.Context, req tassert.JudgeRequest) (tassert.JudgeVerdict, error) {
	return tassert.JudgeVerdict{Pass: strings.Contains(req.Rubric, "pass"), Reason: "judged " + req.Rubric}, nil
}

func (p *fakeProvider) Evaluate(context.Context, tassert.EvalRequest) (tassert.EvalVerdict, error) {
	return tassert.EvalVerdict{Pass: true}, nil
}

func (p *fakeProvider) Usage() model.Usage          { return model.Usage{Calls: p.calls} }
func (p *fakeProvider) Close(context.Context) error { return nil }

type echoTarget struct{ auth string }

func (e *echoTarget) Name() string { return "echo" }

func (e *echoTarget) Start(_ context.Context, rc *target.RunContext) (target.Session, error) {
	auth, err := rc.Render("{{secrets.TOKEN}}")
	if err != nil {
		return nil, err
	}
	e.auth = auth
	return &echoSession{}, nil
}

type echoSession struct{ pending *target.AgentTurn }

func (s *echoSession) Send(_ context.Context, turn target.UserTurn) error {
	now := time.Now()
	s.pending = &target.AgentTurn{Text: "echo: " + turn.Text, StartedAt: now, EndedAt: now}
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

func TestRun(t *testing.T) {
	t.Setenv("TAMESU_TEST_TOKEN", "tok-123")
	out := t.TempDir()
	tgt := &echoTarget{}
	outcome := Run(t, Test{
		Name:     "refund",
		Target:   tgt,
		Persona:  Persona{Name: "Marta", Style: "polite"},
		Scenario: Scenario{Goal: "ask about returns", MaxTurns: 4},
		Repeat:   2,
		Assert: []tassert.Assertion{
			tassert.Contains("echo: can I return shoes?"),
			tassert.Judge("should pass"),
			tassert.Metric("turns").Eq(2),
		},
	}, WithProvider(&fakeProvider{}), WithSecrets(map[string]string{"TOKEN": "env:TAMESU_TEST_TOKEN"}), WithOutDir(out))
	if !outcome.Passed {
		t.Fatalf("expected the test to pass: %+v", outcome.Runs[0].Assertions)
	}
	if len(outcome.Runs) != 2 {
		t.Fatalf("want 2 runs, got %d", len(outcome.Runs))
	}
	if tgt.auth != "tok-123" {
		t.Errorf("the target did not get the secret: %q", tgt.auth)
	}
	for _, f := range []string{"summary.json", "junit.xml", "tests/refund/run-1/result.json", "tests/refund/run-2/transcript.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// recordingTB captures failures instead of failing, so the failure path of
// Run can be asserted.
type recordingTB struct {
	testing.TB
	t      *testing.T
	errors []string
}

func (r *recordingTB) Helper()                         {}
func (r *recordingTB) Name() string                    { return "recording" }
func (r *recordingTB) TempDir() string                 { return r.t.TempDir() }
func (r *recordingTB) Context() context.Context        { return context.Background() }
func (r *recordingTB) Log(args ...any)                 { r.t.Log(args...) }
func (r *recordingTB) Logf(format string, args ...any) { r.t.Logf(format, args...) }
func (r *recordingTB) Errorf(format string, args ...any) {
	r.errors = append(r.errors, strings.TrimSpace(sprintf(format, args...)))
}
func (r *recordingTB) Fatalf(format string, args ...any) { r.t.Fatalf(format, args...) }
func (r *recordingTB) Fatal(args ...any)                 { r.t.Fatal(args...) }

func sprintf(format string, args ...any) string {
	return strings.TrimSpace(fmtSprintf(format, args...))
}

func TestRunFailsTheTest(t *testing.T) {
	t.Setenv("TAMESU_TEST_TOKEN", "tok-123")
	rec := &recordingTB{t: t}
	outcome := Run(rec, Test{Target: &echoTarget{}, Scenario: Scenario{Goal: "g"}, Assert: []tassert.Assertion{tassert.Judge("should fail")}}, WithProvider(&fakeProvider{}), WithOutDir(t.TempDir()), WithSecrets(map[string]string{"TOKEN": "env:TAMESU_TEST_TOKEN"}))
	if outcome.Passed {
		t.Fatal("expected the judge to fail")
	}
	if len(rec.errors) != 2 {
		t.Fatalf("want the assertion and the summary errors, got %q", rec.errors)
	}
	if !strings.Contains(rec.errors[0], "judged should fail") || !strings.Contains(rec.errors[1], "0 of 1 runs passed") {
		t.Errorf("unexpected errors: %q", rec.errors)
	}
}

var fmtSprintf = fmt.Sprintf

// Ground implements assert.Grounder: every claim is supported.
func (f *fakeProvider) Ground(_ context.Context, req tassert.GroundRequest) (tassert.GroundVerdict, error) {
	return tassert.GroundVerdict{Claims: []tassert.Claim{{Text: "fake claim", Supported: true, Evidence: "(fake)"}}, Reason: "fake grounder"}, nil
}
