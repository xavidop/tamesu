package main

import (
	"context"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/realtime"
)

func init() {
	targetFactories[config.TargetRealtime] = newRealtimeTarget
}

// newRealtimeTarget maps a suite's realtime target onto the realtime module's
// config. The mode comes from the runner (the test's, the target's or --mode).
func newRealtimeTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	return realtime.New(spec.Name, realtime.Config{
		Connect: connectPolicy(t),
		URL:     t.URL, Model: t.Model, Headers: t.Headers, SessionUpdate: t.SessionUpdate,
		Protocol: t.Protocol, Mode: spec.Mode, DoneTool: t.DoneTool, Timeout: t.TurnTimeout.Std(),
		ToolResults: t.ToolResults, AudioFormat: t.AudioFormat,
	}), nil
}
