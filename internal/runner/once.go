package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	for k, v := range it.test.Vars {
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
	hookEnv := map[string]string{"TAMESU_SUITE": it.suite.Suite, "TAMESU_TEST": it.test.Name, "TAMESU_RUN": fmt.Sprint(idx + 1), "TAMESU_RUN_DIR": dir, "TAMESU_SESSION": sessionID}
	if hooks := hooksOf(r.o.Project, it.suite, beforeEach); len(hooks) > 0 {
		hr := hookRun{stage: "before_each", hooks: hooks, scope: scope, env: hookEnv, log: filepath.Join(dir, "hooks.log")}
		if err := hr.run(tctx); err != nil {
			return fail(r.classify(tctx, err), err)
		}
	}
	if hooks := hooksOf(r.o.Project, it.suite, afterEach); len(hooks) > 0 {
		defer func() {
			hr := hookRun{stage: "after_each", hooks: hooks, scope: scope, env: hookEnv, log: filepath.Join(dir, "hooks.log")}
			if err := hr.run(context.WithoutCancel(ctx)); err != nil {
				logger.Warn("after_each hooks failed", "error", err)
			}
		}()
	}
	if it.target.Session != nil && it.target.Session.Open != nil {
		caps, err := openSession(tctx, it.target.Session.Open, render, it.timeout, logger)
		if err != nil {
			return fail(r.classify(tctx, err), err)
		}
		for name, v := range caps {
			scope.Values["session."+name] = v
		}
	}
	tgt, err := r.o.Targets(tctx, TargetSpec{Name: it.test.Target, Config: it.target, Suite: it.suite, Project: r.o.Project, Mode: it.mode, Headed: r.o.Headed})
	if err != nil {
		return fail(r.classify(tctx, err), fmt.Errorf("target %s: %w", it.test.Target, err))
	}
	rc := &target.RunContext{TestName: it.test.Name, RunIndex: idx, Dir: dir, Vars: vars, Render: render, Logger: logger, SuiteDir: filepath.Dir(it.suite.Path), UpdateBaselines: r.o.UpdateVisual}
	if cm, ok := r.o.Provider.(target.ChatModel); ok {
		rc.Chat = cm
	}
	if len(it.test.FuzzTools) > 0 {
		rc.ToolFuzz = make(map[string]target.ToolFuzz, len(it.test.FuzzTools))
		for name, f := range it.test.FuzzTools {
			mode := f.Mode
			if m, err := render(mode); err == nil {
				mode = m
			}
			rc.ToolFuzz[name] = target.ToolFuzz{Mode: mode, Delay: time.Duration(f.Delay), Result: f.Result}
		}
	}
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

	var extractFn func(description, text string) (string, bool)
	if ex, ok := r.o.Provider.(model.Extractor); ok && r.o.Provider != nil {
		extractFn = func(description, text string) (string, bool) {
			res, err := ex.Extract(tctx, model.ExtractRequest{Description: description, Text: text})
			if err != nil {
				logger.Warn("capture: extracting a value failed", "description", description, "error", err)
				return "", false
			}
			return res.Value, res.Found
		}
	}
	caps, err := newCapturer(it.test.Capture, func(name, value string) { scope.Values["captured."+name] = value }, extractFn)
	if err != nil {
		return fail(report.KindInfra, err)
	}
	loop := &persona.Loop{Sim: r.simulator(), Seed: it.seed, Mode: it.mode, Logger: logger, Now: r.now, ScriptTimeout: it.timeout / 2, OnAgentTurn: caps.turn, Handoff: handoffDetector(it.target.Handoff)}
	var lo *persona.Outcome
	var loopErr error
	var stepFails []assert.Result
	safe := modelScope(scope)
	renderForModel := func(s string) (string, error) { return config.Render(s, safe) }
	loop.Render = renderForModel
	if bs, ok := sess.(target.BrowserSession); ok {
		lo, stepFails, loopErr = r.runBrowser(tctx, it, bs, dir, out, renderForModel)
	} else if p, err := renderPersona(it.test.Persona, renderForModel); err != nil {
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
	out.Captures = caps.snapshot()
	out.TraceIDs = append(out.TraceIDs, lo.TraceIDs...)
	out.StopReason = lo.StopReason
	out.Events = append(append([]target.Event(nil), lo.Events...), arts.Events...)
	for k, v := range arts.Files {
		if out.Artifacts == nil {
			out.Artifacts = map[string]string{}
		}
		out.Artifacts[k] = v
	}
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
	if hs := assert.Handoffs(&assert.RunResult{Events: out.Events}); len(hs) > 0 && hs[0].Turn > 0 {
		out.Metrics[metrics.HandoffTurn] = float64(hs[0].Turn)
	}
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
		Metrics: out.Metrics, Artifacts: arts, Status: statuses, Responses: responses, Events: out.Events,
		Captures: out.Captures,
	}
	if it.test.Persona != nil {
		rr.Language = it.test.Persona.Language
	}
	if r.o.Provider != nil {
		rr.Judger = r.o.Provider
		rr.Grounder = r.o.Provider
		rr.Evaluator = r.o.Provider
		if e, ok := r.o.Provider.(assert.Embedder); ok {
			rr.Embedder = e
		}
	}
	actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), it.timeout)
	defer acancel()
	out.Passed = true
	if lo.ScriptError != nil {
		out.Assertions = append(out.Assertions, assert.Result{Name: "script", Pass: false, Reason: lo.ScriptError.Error()})
		out.Passed = false
	}
	if len(stepFails) > 0 {
		out.Assertions = append(out.Assertions, stepFails...)
		out.Passed = false
	}
	// Suite-wide rules first: the SLOs, then defaults.assert, then the test's own.
	sloNames := make([]string, 0, len(it.slo))
	for name := range it.slo {
		sloNames = append(sloNames, name)
	}
	sort.Strings(sloNames)
	for _, name := range sloNames {
		res := assert.SLO(name, it.slo[name]).Evaluate(actx, rr)
		out.Assertions = append(out.Assertions, res)
		if !res.Pass {
			out.Passed = false
		}
	}
	all := make([]config.Assertion, 0, len(it.everyAssert)+len(it.test.Assert))
	for _, ca := range it.everyAssert {
		if ca.From == "" {
			ca.From = "defaults"
		} else {
			ca.From = "defaults/" + ca.From
		}
		all = append(all, ca)
	}
	all = append(all, it.test.Assert...)
	for i, ca := range all {
		ca = renderAssertion(ca, renderForModel)
		ca = renderStateAssertion(ca, render)
		var a assert.Assertion
		var err error
		if ca.Golden != nil {
			a, err = r.buildGolden(it, ca.Golden, lo.Transcript)
		} else {
			a, err = Build(ca)
		}
		if err != nil {
			out.Assertions = append(out.Assertions, assert.Result{Name: fmt.Sprintf("assert[%d]", i), Pass: false, Reason: err.Error()})
			out.Passed = false
			continue
		}
		res := a.Evaluate(actx, rr)
		if res.TraceID != "" {
			out.TraceIDs = append(out.TraceIDs, res.TraceID)
		}
		if ca.From != "" {
			res.Name = ca.From + "/" + res.Name
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
	if out.Duration == 0 && !out.StartedAt.IsZero() {
		out.Duration = r.now().Sub(out.StartedAt)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(out.Dir, "result.json"), append(b, '\n'), 0o644)
	}
	if err != nil {
		r.logger.Warn("writing result.json failed", "dir", out.Dir, "error", err)
	}
}
