package main

import (
	"context"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/pipecat"
)

func init() {
	targetFactories[config.TargetPipecat] = newPipecatTarget
}

// newPipecatTarget maps a suite's pipecat target onto the pipecat module's
// config. The mode comes from the runner (the test's, the target's or --mode).
func newPipecatTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	return pipecat.New(spec.Name, pipecat.Config{
		Connect: connectPolicy(t),
		URL:     t.URL, Headers: t.Headers, Mode: spec.Mode, SampleRate: t.SampleRate, Idle: t.Idle.Std(),
		Timeout: t.TurnTimeout.Std(), ToolResults: t.ToolResults,
	}), nil
}
