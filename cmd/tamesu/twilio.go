package main

import (
	"context"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/twilio"
)

func init() {
	targetFactories[config.TargetTwilio] = newTwilioTarget
}

// newTwilioTarget maps a suite's twilio target onto the twilio module's config.
func newTwilioTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	return twilio.New(spec.Name, twilio.Config{
		Connect: connectPolicy(t),
		URL:     t.URL, Headers: t.Headers, Parameters: t.Parameters, From: t.From, To: t.To,
		Idle: t.Idle.Std(), Timeout: t.TurnTimeout.Std(),
	}), nil
}

var _ cli.TargetFactory = newTwilioTarget
