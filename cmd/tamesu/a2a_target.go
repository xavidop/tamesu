package main

import (
	"context"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	a2atarget "github.com/xavidop/tamesu/targets/a2a"
)

func init() {
	targetFactories[config.TargetA2A] = newA2ATarget
}

// newA2ATarget maps a suite's a2a target onto the a2a module's config.
func newA2ATarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	cfg := a2atarget.Config{URL: t.URL, Transport: t.Transport, Headers: t.Headers, Stream: t.Stream, Skill: t.Skill, Metadata: t.Metadata, Timeout: t.TurnTimeout.Std(), Connect: connectPolicy(t)}
	if t.Push != nil && t.Push.Enabled {
		cfg.Push = &a2atarget.Push{Addr: t.Push.Addr, URL: t.Push.URL}
	}
	return a2atarget.New(spec.Name, cfg), nil
}
