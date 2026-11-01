package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/internal/persona"
	"github.com/xavidop/tamesu/metrics"
	"github.com/xavidop/tamesu/report"
	"github.com/xavidop/tamesu/target"
)

// closeGrace is how long a session gets to close and save artifacts after
// the run's own context is done.
const closeGrace = 30 * time.Second

// runWithRetries runs one repeat, retrying infrastructure errors up to the
// test's retries.
func (r *run) runWithRetries(ctx context.Context, it *item, idx int) *report.RunOutcome {
	attempts := it.retries + 1
	var out *report.RunOutcome
	for attempt := 1; attempt <= attempts; attempt++ {
		out = r.runOnce(ctx, it, idx, attempt)
		out.Attempts = attempt
		if out.ErrorKind != report.KindInfra || ctx.Err() != nil || attempt == attempts {
			return out
		}
		r.logger.Warn("retrying after an infrastructure error", "test", it.test.Name, "run", idx+1, "attempt", attempt, "error", out.Error)
	}
	return out
}

// runOnce is one attempt of one repeat: session, persona loop, metrics,
// assertions, run directory.
func (r *run) runOnce(ctx context.Context, it *item, idx, attempt int) *report.RunOutcome {
	start := r.now()
	dir := filepath.Join(it.dir, fmt.Sprintf("run-%d", idx+1))
	out := &report.RunOutcome{Index: idx + 1, Attempt: attempt, StartedAt: start, Dir: dir, Metrics: map[string]float64{}}
	defer func() { out.Duration = r.now().Sub(start) }()
	fail := func(kind string, err error) *report.RunOutcome {
		out.ErrorKind = kind
		out.Error = err.Error()
		r.writeResult(out)
		return out
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(report.KindInfra, fmt.Errorf("create run directory: %w", err))
	}
	if ctx.Err() != nil {
		kind, msg := classifyCancel(ctx)
		return fail(kind, errors.New(msg))
	}

	var usageBefore = r.usageNow()
	tctx, cancel := context.WithTimeout(ctx, it.timeout)
	defer cancel()

	sessionID := uuid.NewString()
	vars := map[string]string{}
	for k, v := range it.suite.Vars {
		vars[k] = v
	}
	for k, v := range r.o.Vars {
		vars[k] = v
	}
	scope := config.Scope{
		Vars:   vars,
		Env:    r.o.Env,
		Values: map[string]string{"session": sessionID, "test": it.test.Name, "sip.inbound_number": r.o.Project.SIP.InboundNumber},
	}
	if r.o.Secrets != nil {
		scope.Secrets = r.o.Secrets.Lookup
	}
	render := func(s string) (string, error) { return config.Render(s, scope) }
	logger := r.logger.With("test", it.test.Name, "run", idx+1)

	if r.o.Targets == nil {
		return fail(report.KindInfra, errors.New("no target factory is available in this build"))
	}
	tgt, err := r.o.Targets(tctx, TargetSpec{Name: it.test.Target, Config: it.target, Suite: it.suite, Mode: it.mode})
	if err != nil {
		return fail(r.classify(tctx, err), fmt.Errorf("target %s: %w", it.test.Target, err))
	}
	rc := &target.RunContext{TestName: it.test.Name, RunIndex: idx, Dir: dir, Vars: vars, Render: render, Logger: logger}
	if it.mode == "audio" {
		if sp, ok := r.o.Provider.(target.Speech); ok {
			rc.Speech = sp
		}
		rc.Voice = voiceOf(it.test.Persona, it.seed, it.target.TranscriptSource)
		rc.Voice.VAD = r.o.Project.Speech.VAD.Provider
	}
	sess, err := tgt.Start(tctx, rc)
	if err != nil {
		return fail(r.classify(tctx, err), fmt.Errorf("start %s: %w", it.test.Target, err))
	}

	loop := &persona.Loop{Sim: r.simulator(), Seed: it.seed, Mode: it.mode, Logger: logger, Now: r.now}
	var lo *persona.Outcome
	var loopErr error
	safe := modelScope(scope)
	renderForModel := func(s string) (string, error) { return config.Render(s, safe) }
	if p, err := renderPersona(it.test.Persona, renderForModel); err != nil {
		lo, loopErr = &persona.Outcome{}, err
	} else if sc, err := renderScenario(it.test.Scenario, renderForModel); err != nil {
		lo, loopErr = &persona.Outcome{}, err
	} else {
		loop.Persona, loop.Scenario = p, sc
		lo, loopErr = loop.Run(tctx, sess)
	}

	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), closeGrace)
	arts, closeErr := sess.Close(closeCtx)
	closeCancel()
	if closeErr != nil {
		logger.Warn("closing the session failed", "error", closeErr)
	}

	out.Transcript = lo.Transcript
	out.TraceIDs = lo.TraceIDs
	out.StopReason = lo.StopReason
	out.Events = arts.Events
	out.Artifacts = arts.Files
	var responses []any
	var statuses []int
	for _, a := range lo.AgentTurns {
		out.ToolCalls = append(out.ToolCalls, a.ToolCalls...)
		if a.Raw != nil {
			responses = append(responses, a.Raw)
		}
		if a.Status != 0 {
			statuses = append(statuses, a.Status)
		}
	}
	metrics.Summarize(out.Metrics, metrics.TurnLatencyMs, lo.LatenciesMs)
	metrics.Summarize(out.Metrics, metrics.TimeToFirstTokenMs, lo.FirstTokenMs)
	out.Metrics[metrics.Turns] = float64(len(lo.AgentTurns))
	for k, v := range arts.Metrics {
		out.Metrics[k] = v
	}
	out.Usage = usageDelta(usageBefore, r.usageNow())
	out.Metrics[metrics.LLMCostUSD] = out.Usage.CostUSD
	out.Metrics[metrics.LLMTokens] = float64(out.Usage.Total())
	r.writeTranscript(dir, out)

	if loopErr != nil {
		kind := r.classify(tctx, loopErr)
		msg := loopErr.Error()
		if kind == report.KindTimeout {
			msg = fmt.Sprintf("timed out after %s (%s)", it.timeout, loopErr)
		}
		return fail(kind, errors.New(msg))
	}

	rr := &assert.RunResult{
		TestName: it.test.Name, Target: it.test.Target, Transcript: lo.Transcript, ToolCalls: out.ToolCalls,
		Metrics: out.Metrics, Artifacts: arts, Status: statuses, Responses: responses, Events: arts.Events,
	}
	if r.o.Provider != nil {
		rr.Judger = r.o.Provider
		rr.Evaluator = r.o.Provider
	}
	actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), it.timeout)
	defer acancel()
	out.Passed = true
	for i, ca := range it.test.Assert {
		a, err := Build(ca)
		if err != nil {
			out.Assertions = append(out.Assertions, assert.Result{Name: fmt.Sprintf("assert[%d]", i), Pass: false, Reason: err.Error()})
			out.Passed = false
			continue
		}
		res := a.Evaluate(actx, rr)
		if res.TraceID != "" {
			out.TraceIDs = append(out.TraceIDs, res.TraceID)
		}
		out.Assertions = append(out.Assertions, res)
		if !res.Pass {
			out.Passed = false
		}
	}
	if out.Assertions == nil {
		out.Assertions = []assert.Result{}
	}
	out.Usage = usageDelta(usageBefore, r.usageNow())
	out.Metrics[metrics.LLMCostUSD] = out.Usage.CostUSD
	out.Metrics[metrics.LLMTokens] = float64(out.Usage.Total())
	r.writeResult(out)
	return out
}

// voiceOf maps a persona's voice and behaviors onto the target's Voice.
func voiceOf(p *config.Persona, seed int64, transcriptSource string) *target.Voice {
	v := &target.Voice{Seed: seed, NoiseSNRdB: 20, TranscriptSource: transcriptSource}
	if p == nil {
		return v
	}
	v.Hint, v.Language = p.Voice, p.Language
	if b := p.Behaviors; b != nil {
		v.InterruptProbability = b.InterruptProbability
		if len(b.SilenceAfterTurnMs) == 2 {
			v.SilenceMin = time.Duration(b.SilenceAfterTurnMs[0]) * time.Millisecond
			v.SilenceMax = time.Duration(b.SilenceAfterTurnMs[1]) * time.Millisecond
		}
		v.BackgroundNoise, v.LineQuality = b.BackgroundNoise, b.LineQuality
		if b.BackgroundNoiseSNRdB != nil {
			v.NoiseSNRdB = *b.BackgroundNoiseSNRdB
		}
	}
	return v
}

func (r *run) simulator() model.Simulator {
	if r.o.Provider == nil {
		return nil
	}
	return r.o.Provider
}

func (r *run) usageNow() model.Usage {
	if r.o.Provider == nil {
		return model.Usage{}
	}
	return r.o.Provider.Usage()
}

// classify maps an error of a run to its kind: the budget or an interrupt
// when the run context is done, a timeout when the test's own deadline
// passed, infrastructure otherwise.
func (r *run) classify(tctx context.Context, err error) string {
	if cause := context.Cause(tctx); cause != nil {
		if errors.Is(cause, ErrBudgetExceeded) {
			return report.KindBudget
		}
		if errors.Is(cause, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return report.KindTimeout
		}
		if errors.Is(cause, context.Canceled) {
			return report.KindCancelled
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return report.KindTimeout
	}
	return report.KindInfra
}

func (r *run) writeTranscript(dir string, out *report.RunOutcome) {
	doc := map[string]any{"turns": out.Transcript, "tool_calls": out.ToolCalls, "stop_reason": out.StopReason}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "transcript.json"), append(b, '\n'), 0o644)
	}
	if err != nil {
		r.logger.Warn("writing transcript.json failed", "dir", dir, "error", err)
	}
}

func (r *run) writeResult(out *report.RunOutcome) {
	if out.Dir == "" {
		return
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(out.Dir, "result.json"), append(b, '\n'), 0o644)
	}
	if err != nil {
		r.logger.Warn("writing result.json failed", "dir", out.Dir, "error", err)
	}
}
