package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/internal/persona"
	"github.com/xavidop/tamesu/metrics"
	"github.com/xavidop/tamesu/report"
	"github.com/xavidop/tamesu/target"
)

// ErrNoBrowserAgent is returned when a goal or expect step runs without a model.
var ErrNoBrowserAgent = errors.New("no browser agent model is available: goal and expect steps need the llm module and models.browser_agent (or models.default)")

// runBrowser plays a browser test's steps on the session and returns what the
// persona loop would: a transcript (one user line per step, one agent line per
// outcome) and a stop reason, plus the failed steps as assertion results.
func (r *run) runBrowser(ctx context.Context, it *item, bs target.BrowserSession, dir string, out *report.RunOutcome, renderForModel func(string) (string, error)) (*persona.Outcome, []assert.Result, error) {
	page := bs.Page()
	lo := &persona.Outcome{}
	var fails []assert.Result
	agent, _ := r.o.Provider.(model.BrowserAgent)
	goalSteps := 0
	for i, st := range it.test.Steps {
		rec := report.StepRecord{Index: i + 1, Kind: st.Kind(), StartedAt: r.now()}
		var text string
		switch st.Kind() {
		case "goal":
			text = st.Goal
		case "expect":
			text = st.Expect
		case "action":
			b, _ := json.Marshal(st.Action)
			text = string(b)
		}
		rec.Text = text
		lo.Transcript = append(lo.Transcript, assert.Turn{Role: "user", Text: text, At: rec.StartedAt})
		var err error
		switch st.Kind() {
		case "goal":
			var goal string
			if goal, err = renderForModel(st.Goal); err == nil {
				if agent == nil {
					err = ErrNoBrowserAgent
				} else {
					var res model.GoalResult
					res, err = agent.RunGoal(ctx, model.GoalRequest{Goal: goal, Page: page, MaxSteps: maxSteps(it), AllowedDomains: it.target.AllowedDomains, Screenshots: false})
					rec.Success, rec.Reason, rec.Steps, rec.Actions = res.Success, res.Reason, res.Steps, res.Actions
					goalSteps += res.Steps
					if res.TraceID != "" {
						lo.TraceIDs = append(lo.TraceIDs, res.TraceID)
					}
				}
			}
		case "expect":
			var expectation string
			if expectation, err = renderForModel(st.Expect); err == nil {
				if agent == nil {
					err = ErrNoBrowserAgent
				} else {
					var res model.ExpectResult
					res, err = agent.CheckExpect(ctx, page, expectation)
					rec.Success, rec.Reason = res.Pass, res.Reason
					if res.TraceID != "" {
						lo.TraceIDs = append(lo.TraceIDs, res.TraceID)
					}
				}
			}
		case "action":
			var did string
			did, err = page.Do(ctx, st.Action)
			rec.Success, rec.Reason = err == nil, did
			var se *target.StepError
			if errors.As(err, &se) {
				rec.Success, rec.Reason, err = false, se.Reason, nil
			}
		default:
			err = fmt.Errorf("step %d is not exactly one of goal, expect, action", i+1)
		}
		rec.EndedAt = r.now()
		if shot, serr := page.Screenshot(ctx); serr == nil && dir != "" {
			rel := filepath.Join("screenshots", fmt.Sprintf("step-%02d.png", i+1))
			if os.MkdirAll(filepath.Join(dir, "screenshots"), 0o755) == nil && os.WriteFile(filepath.Join(dir, rel), shot, 0o644) == nil {
				rec.Screenshot = rel
				if out.Artifacts == nil {
					out.Artifacts = map[string]string{}
				}
				out.Artifacts[rel] = rel
			}
		}
		if err != nil {
			rec.Success = false
			rec.Reason = err.Error()
			out.Steps = append(out.Steps, rec)
			lo.Transcript = append(lo.Transcript, assert.Turn{Role: "agent", Text: rec.Reason, At: rec.EndedAt, Index: i + 1})
			lo.StopReason = "step_error"
			writeSteps(dir, out.Steps)
			return lo, fails, fmt.Errorf("step %d (%s): %w", i+1, st.Kind(), err)
		}
		out.Steps = append(out.Steps, rec)
		lo.Transcript = append(lo.Transcript, assert.Turn{Role: "agent", Text: rec.Reason, At: rec.EndedAt, Index: i + 1})
		if !rec.Success {
			fails = append(fails, assert.Result{Name: fmt.Sprintf("step[%d] %s", i+1, st.Kind()), Pass: false, Reason: rec.Reason})
			lo.StopReason = "step_failed"
			break
		}
	}
	if lo.StopReason == "" {
		lo.StopReason = "steps_done"
	}
	out.Metrics[metrics.GoalSteps] = float64(goalSteps)
	writeSteps(dir, out.Steps)
	return lo, fails, nil
}

func maxSteps(it *item) int { return it.maxGoalSteps }

func writeSteps(dir string, steps []report.StepRecord) {
	if dir == "" {
		return
	}
	b, err := json.MarshalIndent(steps, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(dir, "steps.json"), append(b, '\n'), 0o644)
	}
}
